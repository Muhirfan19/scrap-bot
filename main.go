package main

// ============================================================================
// CACING JADI NAGA - Go / Bybit v5 REST edition
//
// M1 support/resistance scalper for 1000BONK/USDT:USDT (linear perpetual).
// All-in compounding position sizing, market-order entries, native
// exchange-side hard stop-loss + trailing stop, and an order-book reversal
// bail-out as a second, faster layer of protection.
//
// Run with: go build -o naga-bot-go . && ./naga-bot-go
// Config is loaded from ".env" in the working directory (see .env.example).
// ============================================================================

import (
	"fmt"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"
	"time"
)

const klineInterval = "1" // Bybit v5 kline interval string for 1-minute candles

func main() {
	cfg := LoadConfig()

	if cfg.APIKey == "" || cfg.APISecret == "" {
		fmt.Println("FATAL: BYBIT_API_KEY / BYBIT_API_SECRET missing. Set them in .env or the environment.")
		os.Exit(1)
	}
	if strings.ContainsAny(cfg.Symbol, "/:") {
		fmt.Printf("FATAL: SYMBOL %q looks like a CCXT-style symbol. Bybit's raw v5 API wants the bare "+
			"form, e.g. \"1000BONKUSDT\" - no slashes or colons.\n", cfg.Symbol)
		os.Exit(1)
	}

	printBanner(cfg)

	client := NewBybitClient(cfg)

	// Graceful shutdown on Ctrl+C / SIGTERM: stop the loop cleanly, but -
	// same as every version of this bot before - we do NOT auto-close any
	// open position on exit. The native stop-loss/trailing-stop stays
	// active on Bybit's side regardless, so the position remains protected
	// even after this process exits.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		logf("[SHUTDOWN] Signal received. Exiting cleanly. Any open position stays open on Bybit - " +
			"its native SL/trailing-stop keeps protecting it. Check the app if you need to close manually.")
		os.Exit(0)
	}()

	logf("[INIT] Fetching instrument info for %s...", cfg.Symbol)
	instrument, err := client.GetInstrumentInfo(cfg.Category, cfg.Symbol)
	if err != nil {
		fmt.Printf("FATAL: could not fetch instrument info for %s: %v\n", cfg.Symbol, err)
		os.Exit(1)
	}
	logf("[INIT] %s: qtyStep=%v minOrderQty=%v priceTick=%v exchangeMaxLeverage=%v",
		cfg.Symbol, instrument.QtyStep, instrument.MinOrderQty, instrument.PriceTick, instrument.MaxLeverage)

	effectiveLeverage := cfg.Leverage
	if instrument.MaxLeverage > 0 && float64(effectiveLeverage) > instrument.MaxLeverage {
		effectiveLeverage = int(instrument.MaxLeverage)
		logf("[INIT] Requested leverage %dx exceeds exchange max %.0fx for %s - clipping to %dx.",
			cfg.Leverage, instrument.MaxLeverage, cfg.Symbol, effectiveLeverage)
		cfg.Leverage = effectiveLeverage
	}

	logf("[INIT] Setting leverage to %dx...", cfg.Leverage)
	if err := client.SetLeverage(cfg.Category, cfg.Symbol, cfg.Leverage); err != nil {
		fmt.Printf("FATAL: could not set leverage: %v\n", err)
		os.Exit(1)
	}

	free, err := client.GetFreeUSDT()
	if err != nil {
		logf("[WARN] Could not fetch starting free balance: %v", err)
	} else {
		logf("[INIT] Starting free balance: %.4f USDT", free)
	}

	logf("[INIT] Entering unbreakable hunting loop. Ctrl+C to stop gracefully.")
	huntLoop(client, cfg, instrument)
}

func printBanner(cfg Config) {
	fmt.Println(strings.Repeat("=", 78))
	fmt.Println(" CACING JADI NAGA - Go / Bybit v5 REST edition")
	fmt.Printf(" Symbol=%s | Leverage(requested)=%dx | Testnet=%v\n", cfg.Symbol, cfg.Leverage, cfg.UseTestnet)
	fmt.Printf(" PositionPct=%.0f%% | TP=%.2f%% | SL=%.2fx ATR | Trail=%.2fx ATR\n",
		cfg.PositionPct*100, cfg.TPPct*100, cfg.SLATRMult, cfg.TrailATRMult)
	fmt.Printf(" EntryOrderType=%s | RequireEMAConfirm=%v | RequireOrderbookConfirm=%v (ratio=%.2f)\n",
		cfg.EntryOrderType, cfg.RequireEMAConfirm, cfg.RequireOrderbookEntryConfirm, cfg.OrderbookEntryRatio)
	fmt.Println(strings.Repeat("=", 78))
}

// huntLoop is the unbreakable `for {}` at the heart of the bot. Every
// iteration is wrapped in a panic recovery so that ANY unexpected error -
// a nil pointer, an unhandled edge case, whatever - logs a full stack
// trace and keeps the process alive instead of crashing it. Combined with
// the HTTP-level retry/backoff in bybit.go, this is what makes the bot
// resilient to network hiccups, rate limits, and its own bugs alike.
func huntLoop(client *BybitClient, cfg Config, instrument InstrumentInfo) {
	scanInterval := time.Duration(cfg.ScanIntervalMs) * time.Millisecond

	for {
		func() {
			defer func() {
				if r := recover(); r != nil {
					logf("[LOOP GUARD] Recovered from panic: %v\n%s", r, debug.Stack())
					time.Sleep(1 * time.Second)
				}
			}()
			runOneCycle(client, cfg, instrument, scanInterval)
		}()
	}
}

// runOneCycle handles exactly one pass of the loop: if a position exists,
// re-attach management to it (covers a bot restart mid-trade); otherwise
// scan for a new signal and enter if one clears every gate.
func runOneCycle(client *BybitClient, cfg Config, instrument InstrumentInfo, scanInterval time.Duration) {
	pos, err := client.GetPosition(cfg.Category, cfg.Symbol)
	if err != nil {
		logf("[WARN] fetch position failed: %v", err)
		time.Sleep(scanInterval)
		return
	}

	if pos.IsOpen() {
		side := "long"
		if pos.Side == "Sell" {
			side = "short"
		}
		klines, err := client.GetKline(cfg.Category, cfg.Symbol, klineInterval, minKlineLimit(cfg))
		if err != nil || len(klines) < cfg.ATRPeriod+2 {
			logf("[WARN] could not recompute ATR for existing position (%v); retrying shortly.", err)
			time.Sleep(scanInterval)
			return
		}
		closed := klines[:len(klines)-1]
		atr, ok := ATR(closed, cfg.ATRPeriod)
		if !ok || atr <= 0 {
			logf("[WARN] ATR unavailable for existing position; retrying shortly.")
			time.Sleep(scanInterval)
			return
		}
		ManagePosition(client, cfg, instrument, side, pos.EntryPrice, pos.Size, atr)
		return
	}

	klines, err := client.GetKline(cfg.Category, cfg.Symbol, klineInterval, minKlineLimit(cfg))
	if err != nil {
		logf("[WARN] fetch kline failed: %v", err)
		time.Sleep(scanInterval)
		return
	}
	if len(klines) == 0 {
		time.Sleep(scanInterval)
		return
	}

	sig := DetectSetup(klines, cfg)
	if sig == nil {
		time.Sleep(scanInterval)
		return
	}

	logf("[SIGNAL] %s %s score=%.2f structure=%.8f atr=%.8f",
		cfg.Symbol, strings.ToUpper(sig.Side), sig.Score, sig.Structure, sig.ATR)

	// Final gate: order book must at least mildly favor this direction
	// right now. Checked only for the winning candidate to keep API usage
	// light (mirrors the Python version's approach).
	if !orderbookConfirms(client, cfg, sig.Side) {
		logf("[SIGNAL SKIP] %s: order book does not favor %s right now, skipping.",
			cfg.Symbol, strings.ToUpper(sig.Side))
		time.Sleep(scanInterval)
		return
	}

	price, err := client.GetLastPrice(cfg.Category, cfg.Symbol)
	if err != nil {
		logf("[WARN] fetch last price for entry failed: %v", err)
		time.Sleep(scanInterval)
		return
	}

	_, err = OpenPosition(client, cfg, instrument, sig.Side, price)
	if err != nil {
		logf("[SKIP] entry failed: %v", err)
		time.Sleep(scanInterval)
		return
	}

	// Re-confirm the fill via GetPosition (ground truth) rather than
	// trusting our own requested qty - a market order can fill at a
	// slightly different price/qty than the snapshot we sized against.
	time.Sleep(500 * time.Millisecond)
	fresh, err := client.GetPosition(cfg.Category, cfg.Symbol)
	if err != nil || !fresh.IsOpen() {
		logf("[SYNC] no position found right after entry order - assuming it never actually opened " +
			"or was instantly closed. Resuming hunt.")
		return
	}

	side := "long"
	if fresh.Side == "Sell" {
		side = "short"
	}
	ManagePosition(client, cfg, instrument, side, fresh.EntryPrice, fresh.Size, sig.ATR)
}

// minKlineLimit ensures we always fetch enough candles for both the
// support/resistance lookback and the ATR period, with a sane floor.
func minKlineLimit(cfg Config) int {
	n := cfg.Lookback + cfg.ATRPeriod + 5
	if n < 45 {
		n = 45
	}
	return n
}
