package main

// ============================================================================
// POSITION SIZING & ORDER EXECUTION
//
// Implements the "Cacing Jadi Naga" all-in compounding rule: every entry
// uses PositionPct (default 1.0 = 100%) of the CURRENT free USDT balance,
// fetched fresh right before sizing - so a winning streak compounds into
// bigger and bigger positions automatically, with no manual intervention.
// ============================================================================

import (
	"fmt"
	"math"
	"strconv"
)

// decimalsFromStep infers how many decimal places to format a qty/price
// string with, based on the exchange's step/tick size (e.g. 0.001 -> 3).
func decimalsFromStep(step float64) int {
	if step <= 0 {
		return 0
	}
	s := strconv.FormatFloat(step, 'f', -1, 64)
	dot := -1
	for i, ch := range s {
		if ch == '.' {
			dot = i
			break
		}
	}
	if dot == -1 {
		return 0
	}
	return len(s) - dot - 1
}

// roundDownToStep floors qty to the nearest multiple of step - Bybit
// rejects orders that don't align to the instrument's lot size.
func roundDownToStep(qty, step float64) float64 {
	if step <= 0 {
		return qty
	}
	return math.Floor(qty/step) * step
}

// roundToTick rounds a price to the nearest multiple of the instrument's
// price tick.
func roundToTick(price, tick float64) float64 {
	if tick <= 0 {
		return price
	}
	return math.Round(price/tick) * tick
}

// SizeResult carries the computed order quantity plus the balance snapshot
// used to compute it, for logging.
type SizeResult struct {
	Qty         float64
	FreeBalance float64
}

// CalcPositionSize fetches the LIVE free USDT balance and applies
// PositionPct * Leverage to determine how large a position to open at the
// given price. This is where compounding happens automatically: a bigger
// balance (from a prior win) directly produces a bigger next position.
func CalcPositionSize(client *BybitClient, cfg Config, instrument InstrumentInfo, price float64) (SizeResult, error) {
	free, err := client.GetFreeUSDT()
	if err != nil {
		return SizeResult{}, fmt.Errorf("fetch free balance: %w", err)
	}

	if free < cfg.MinFreeBalance {
		return SizeResult{}, fmt.Errorf(
			"free balance %.4f USDT is below the configured minimum %.4f - refusing to trade dust amounts that Bybit would reject anyway",
			free, cfg.MinFreeBalance)
	}

	pct := cfg.PositionPct
	if pct > 1.0 {
		pct = 1.0 // hard safety ceiling - never allowed to exceed 100% even by config mistake
	}

	margin := free * pct
	notional := margin * float64(cfg.Leverage)
	rawQty := notional / price

	qty := roundDownToStep(rawQty, instrument.QtyStep)
	if qty < instrument.MinOrderQty {
		return SizeResult{}, fmt.Errorf(
			"computed qty %.8f is below exchange minimum %.8f - balance too small for %s at current price",
			qty, instrument.MinOrderQty, cfg.Symbol)
	}

	return SizeResult{Qty: qty, FreeBalance: free}, nil
}

// OpenPosition fires a market entry order, sized via CalcPositionSize.
// Returns the filled qty (best-effort - Bybit market orders fill near-
// instantly, so we treat the requested qty as the fill and re-confirm via
// GetPosition immediately after in the caller).
func OpenPosition(client *BybitClient, cfg Config, instrument InstrumentInfo, side string, price float64) (float64, error) {
	sized, err := CalcPositionSize(client, cfg, instrument, price)
	if err != nil {
		return 0, err
	}

	orderSide := "Buy"
	if side == "short" {
		orderSide = "Sell"
	}

	qtyDecimals := decimalsFromStep(instrument.QtyStep)
	qtyStr := formatFloat(sized.Qty, qtyDecimals)

	logf("[ENTRY] MARKET %s %s qty=%s (leverage=%dx, balance=%.4f USDT)",
		orderSide, cfg.Symbol, qtyStr, cfg.Leverage, sized.FreeBalance)

	_, err = client.PlaceMarketOrder(PlaceMarketOrderParams{
		Category:   cfg.Category,
		Symbol:     cfg.Symbol,
		Side:       orderSide,
		Qty:        qtyStr,
		ReduceOnly: false,
	})
	if err != nil {
		return 0, fmt.Errorf("place market entry: %w", err)
	}
	return sized.Qty, nil
}

// ClosePosition fires a reduce-only market order to exit all or part of an
// open position - used for take-profit and the order-book reversal
// bail-out (the trailing/hard stop-loss is handled natively by Bybit via
// SetTradingStop instead, so it survives a VPS disconnect).
func ClosePosition(client *BybitClient, cfg Config, instrument InstrumentInfo, side string, qty float64, reason string) error {
	closeSide := "Sell"
	if side == "short" {
		closeSide = "Buy"
	}
	qtyDecimals := decimalsFromStep(instrument.QtyStep)
	qtyStr := formatFloat(qty, qtyDecimals)

	logf("[EXIT] MARKET REDUCE-ONLY %s %s qty=%s | reason=%s", closeSide, cfg.Symbol, qtyStr, reason)

	_, err := client.PlaceMarketOrder(PlaceMarketOrderParams{
		Category:   cfg.Category,
		Symbol:     cfg.Symbol,
		Side:       closeSide,
		Qty:        qtyStr,
		ReduceOnly: true,
	})
	if err != nil {
		return fmt.Errorf("place reduce-only exit: %w", err)
	}
	return nil
}
