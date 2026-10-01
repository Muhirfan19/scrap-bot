package main

// ============================================================================
// STRATEGY - support/resistance pullback scalper.
//
// Only fully CLOSED candles define structure (the currently-forming candle
// is never used as a fake confirmation). A signal requires ALL of:
//   1. Price wicking into a support/resistance zone with a rejection candle
//      (candle body + wick ratio + volume participation).
//   2. EMA trend not working hard against the trade direction (a SOFT
//      filter - see emaConfirms comment for why it isn't a hard gate).
//   3. Order book mildly favoring the trade direction (checked separately,
//      only for the best-scoring candidate, to keep API usage light).
// ============================================================================

import "math"

// Signal describes one actionable setup.
type Signal struct {
	Side      string // "long" or "short"
	Score     float64
	Structure float64 // the support/resistance level that triggered this
	ATR       float64
}

// emaConfirms is a SOFT trend filter. An earlier version required price to
// already be on the favorable side of the EMA, which directly conflicts
// with a support-bounce entry (price at support is usually BELOW the EMA
// by definition - support is a recent low). That made signals extremely
// rare. This version only blocks a trade if the EMA is still moving hard
// AGAINST the trade direction (catching a falling knife); flat or mildly
// adverse EMA is allowed, since that's what a real bounce looks like
// before the trend has actually turned - EMA is a lagging indicator, so
// demanding full confirmation from it defeats an early-catch strategy.
func emaConfirms(closed []Kline, side string, cfg Config) bool {
	if !cfg.RequireEMAConfirm {
		return true
	}
	vals := EMASeries(closed, cfg.EMAPeriod)
	if vals == nil || len(vals) <= cfg.EMASlopeLookback {
		return true // not enough history yet - fail open, don't block on missing data
	}
	current := vals[len(vals)-1]
	prior := vals[len(vals)-1-cfg.EMASlopeLookback]
	if prior == 0 {
		return true
	}
	slope := (current - prior) / prior
	if side == "long" {
		return slope >= -cfg.EMASlopeTolerance
	}
	return slope <= cfg.EMASlopeTolerance
}

// orderbookConfirms is the "order book bagus" gate - bid/ask volume just
// needs to mildly favor the trade direction. Deliberately soft
// (OrderbookEntryRatio defaults to 1.01) so it filters obviously-wrong
// setups without blocking most real ones. On any order-book fetch failure
// it fails OPEN (doesn't block the trade) rather than silently stalling
// the bot on a transient API hiccup.
func orderbookConfirms(client *BybitClient, cfg Config, side string) bool {
	if !cfg.RequireOrderbookEntryConfirm {
		return true
	}
	ob, err := client.GetOrderBook(cfg.Category, cfg.Symbol, cfg.OrderbookDepth)
	if err != nil || len(ob.Bids) == 0 || len(ob.Asks) == 0 {
		return true
	}
	bidVol, askVol := sumVolume(ob.Bids), sumVolume(ob.Asks)
	if bidVol <= 0 || askVol <= 0 {
		return true
	}
	if side == "long" {
		return bidVol >= askVol*cfg.OrderbookEntryRatio
	}
	return askVol >= bidVol*cfg.OrderbookEntryRatio
}

func sumVolume(levels []OrderBookLevel) float64 {
	sum := 0.0
	for _, l := range levels {
		sum += l.Size
	}
	return sum
}

// orderbookFlippedAgainst is the STRICTER check used during trade
// management for the reversal bail-out (not entry) - the opposite side
// must dominate by OrderbookFlipRatio, a much higher bar than the mild
// entry confirmation above.
func orderbookFlippedAgainst(ob OrderBook, side string, flipRatio float64) bool {
	if len(ob.Bids) == 0 || len(ob.Asks) == 0 {
		return false
	}
	bidVol, askVol := sumVolume(ob.Bids), sumVolume(ob.Asks)
	if bidVol <= 0 || askVol <= 0 {
		return false
	}
	if side == "long" {
		return askVol > bidVol*flipRatio
	}
	return bidVol > askVol*flipRatio
}

// DetectSetup evaluates the most recent CLOSED candle against rolling
// support/resistance + ATR zone + wick/volume rejection criteria, returning
// the best-scoring signal (long or short) or nil if nothing qualifies.
//
// `klines` should include the currently-forming candle as the last element
// (as returned by GetKline) - it gets excluded internally so only closed
// data drives the decision.
func DetectSetup(klines []Kline, cfg Config) *Signal {
	minNeeded := cfg.Lookback + cfg.ATRPeriod + 2
	if len(klines) < minNeeded {
		return nil
	}
	closed := klines[:len(klines)-1] // drop the still-forming candle

	atr, ok := ATR(closed, cfg.ATRPeriod)
	if !ok || atr <= 0 {
		return nil
	}

	last := closed[len(closed)-1]
	window := closed[len(closed)-cfg.Lookback:]

	support := window[0].Low
	resistance := window[0].High
	for _, k := range window {
		if k.Low < support {
			support = k.Low
		}
		if k.High > resistance {
			resistance = k.High
		}
	}

	rng := math.Max(last.High-last.Low, 1e-12)
	lowerWick := math.Min(last.Open, last.Close) - last.Low
	upperWick := last.High - math.Max(last.Open, last.Close)
	price := last.Close
	zone := math.Max(atr*cfg.ZoneATRMult, price*0.0007)

	// Volume confirmation - deliberately moderate, this is a pullback
	// scalper, not a "wait for giant breakout volume" bot.
	volLookback := 20
	if len(closed) < volLookback {
		volLookback = len(closed)
	}
	volSum := 0.0
	for _, k := range closed[len(closed)-volLookback:] {
		volSum += k.Volume
	}
	avgVol := volSum / float64(volLookback)
	volRatio := 0.0
	if avgVol > 0 {
		volRatio = last.Volume / avgVol
	}

	nearSupport := math.Abs(price-support) <= zone
	nearResistance := math.Abs(resistance-price) <= zone

	var best *Signal

	if nearSupport && last.Close >= last.Open &&
		lowerWick/rng >= cfg.MinWickRatio && volRatio >= cfg.MinVolumeRatio &&
		emaConfirms(closed, "long", cfg) {
		score := 1.0 + math.Min(1.0, lowerWick/rng) +
			math.Min(0.7, math.Max(0, 1-math.Abs(price-support)/zone))
		if score >= cfg.MinScore {
			best = &Signal{Side: "long", Score: score, Structure: support, ATR: atr}
		}
	}

	if nearResistance && last.Close <= last.Open &&
		upperWick/rng >= cfg.MinWickRatio && volRatio >= cfg.MinVolumeRatio &&
		emaConfirms(closed, "short", cfg) {
		score := 1.0 + math.Min(1.0, upperWick/rng) +
			math.Min(0.7, math.Max(0, 1-math.Abs(resistance-price)/zone))
		if score >= cfg.MinScore && (best == nil || score > best.Score) {
			best = &Signal{Side: "short", Score: score, Structure: resistance, ATR: atr}
		}
	}

	return best
}
