package main

// ============================================================================
// TRADE MANAGEMENT - "dua jaga" (two guards):
//
//   1. HARD STOP-LOSS + NATIVE TRAILING STOP, set on the Bybit exchange
//      itself via SetTradingStop immediately after entry. Because this
//      lives on Bybit's servers (not in this bot's process), it keeps
//      protecting the position even if the VPS loses network connectivity,
//      crashes, or gets rebooted mid-trade. The trailing stop is updated
//      by re-calling SetTradingStop with a wider trailing distance only
//      when price has moved favorably enough to matter - but even between
//      updates, Bybit's own trailing-stop engine is already active.
//
//   2. ORDER-BOOK REVERSAL BAIL-OUT - a faster, software-side exit that
//      can fire before price even reaches the trailing stop, if bid/ask
//      volume flips hard against the position while it's in profit.
//
// Take-profit is a simple price-target check against the live ticker,
// closed via a reduce-only market order for a guaranteed fast exit.
// ============================================================================

import (
	"time"
)

// ManagePosition runs the full lifecycle of one open trade: attach the
// initial native stop-loss, then loop until TP, SL, or the reversal
// bail-out closes it (or the position disappears - e.g. the user closed it
// manually from the app, which we detect via GetPosition each tick).
func ManagePosition(client *BybitClient, cfg Config, instrument InstrumentInfo, side string, entryPrice, qty, atr float64) {
	var sl float64
	if side == "long" {
		sl = entryPrice - atr*cfg.SLATRMult
	} else {
		sl = entryPrice + atr*cfg.SLATRMult
	}
	sl = roundToTick(sl, instrument.PriceTick)

	var tp float64
	if side == "long" {
		tp = entryPrice * (1 + cfg.TPPct)
	} else {
		tp = entryPrice * (1 - cfg.TPPct)
	}
	tp = roundToTick(tp, instrument.PriceTick)

	priceDecimals := decimalsFromStep(instrument.PriceTick)
	slStr := formatFloat(sl, priceDecimals)

	// Attach the hard stop-loss on the exchange FIRST, before anything
	// else. If this fails, don't run the position unprotected - close it
	// immediately at market instead.
	if err := client.SetTradingStop(cfg.Category, cfg.Symbol, 0, slStr, ""); err != nil {
		logf("[SAFETY] Native SL placement failed (%v); closing position rather than running unprotected.", err)
		_ = ClosePosition(client, cfg, instrument, side, qty, "SL_PLACEMENT_FAILED")
		return
	}

	logf("[MANAGE] %s %s entry=%.8f TP=%.8f initial_SL=%.8f (trailing %.2fx ATR active)",
		cfg.Symbol, side, entryPrice, tp, sl, cfg.TrailATRMult)

	bestPrice := entryPrice
	beArmed := false
	minStep := atr * cfg.TrailMinStepATR
	currentSL := sl

	ticker := time.NewTicker(time.Duration(cfg.ManageIntervalMs) * time.Millisecond)
	defer ticker.Stop()

	for range ticker.C {
		pos, err := client.GetPosition(cfg.Category, cfg.Symbol)
		if err != nil {
			logf("[WARN] manage: fetch position failed: %v", err)
			continue
		}
		if !pos.IsOpen() {
			// Position is gone - either our own TP/SL/bail-out closed it,
			// or the user closed it manually from the Bybit app. Either
			// way, stop managing and let the caller resume hunting.
			logf("[SYNC] %s position no longer open - resuming hunt.", cfg.Symbol)
			return
		}

		price, err := client.GetLastPrice(cfg.Category, cfg.Symbol)
		if err != nil {
			logf("[WARN] manage: fetch last price failed: %v", err)
			continue
		}

		var pnl float64
		if side == "long" {
			pnl = (price - entryPrice) / entryPrice
		} else {
			pnl = (entryPrice - price) / entryPrice
		}

		if !beArmed && pnl >= cfg.BETriggerPct {
			beArmed = true
			logf("[BE] %s reached +%.3f%%; trailing stop now active behind price.", cfg.Symbol, pnl*100)
		}

		// --- Running / trailing stop: pull the stop toward price, never
		// away from it. We track the best price ourselves so we only
		// bother calling SetTradingStop when the improvement clears
		// TrailMinStepATR (avoids spamming the API on every tick). ---
		var shouldTrail bool
		var trailCandidate float64
		if side == "long" {
			if price > bestPrice {
				bestPrice = price
			}
			trailCandidate = bestPrice - atr*cfg.TrailATRMult
			shouldTrail = trailCandidate > currentSL+minStep
		} else {
			if price < bestPrice {
				bestPrice = price
			}
			trailCandidate = bestPrice + atr*cfg.TrailATRMult
			shouldTrail = trailCandidate < currentSL-minStep
		}

		if shouldTrail {
			newSL := roundToTick(trailCandidate, instrument.PriceTick)
			newSLStr := formatFloat(newSL, priceDecimals)
			if err := client.SetTradingStop(cfg.Category, cfg.Symbol, 0, newSLStr, ""); err != nil {
				logf("[WARN] %s: failed to trail SL to %.8f (%v); keeping previous stop %.8f active.",
					cfg.Symbol, newSL, err, currentSL)
			} else {
				logf("[TRAIL] %s SL moved %.8f -> %.8f (best_price=%.8f)", cfg.Symbol, currentSL, newSL, bestPrice)
				currentSL = newSL
			}
		}

		// --- Take profit ---
		if (side == "long" && price >= tp) || (side == "short" && price <= tp) {
			logf("[TP HIT] %s price=%.8f target=%.8f", cfg.Symbol, price, tp)
			if err := ClosePosition(client, cfg, instrument, side, pos.Size, "TAKE_PROFIT"); err != nil {
				logf("[ERROR] TP close failed: %v", err)
			}
			return
		}

		// --- Order-book reversal bail-out: the second "jaga" - can fire
		// before price even reaches the trailing stop level. ---
		ob, err := client.GetOrderBook(cfg.Category, cfg.Symbol, cfg.OrderbookDepth)
		if err == nil && pnl > 0 && orderbookFlippedAgainst(ob, side, cfg.OrderbookFlipRatio) {
			logf("[BAIL-OUT] %s order book flipped hard against %s while +%.3f%% green - securing profit now.",
				cfg.Symbol, side, pnl*100)
			if err := ClosePosition(client, cfg, instrument, side, pos.Size, "ORDERBOOK_REVERSAL"); err != nil {
				logf("[ERROR] reversal bail-out close failed: %v", err)
			}
			return
		}
	}
}
