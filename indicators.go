package main

// ============================================================================
// INDICATORS - ATR (Average True Range) and EMA (Exponential Moving Average),
// computed directly from Kline slices. Both operate on CLOSED candles only;
// callers are responsible for excluding the still-forming candle before
// passing data in here (see strategy.go).
// ============================================================================

// ATR computes the Average True Range over the last `period` candles.
// Returns 0 and false if there isn't enough history.
func ATR(klines []Kline, period int) (float64, bool) {
	if len(klines) < period+1 {
		return 0, false
	}
	start := len(klines) - period
	sum := 0.0
	for i := start; i < len(klines); i++ {
		high := klines[i].High
		low := klines[i].Low
		prevClose := klines[i-1].Close
		tr := max3(high-low, absF(high-prevClose), absF(low-prevClose))
		sum += tr
	}
	return sum / float64(period), true
}

// EMASeries computes the full EMA series over the given closes, using the
// standard smoothing factor k = 2/(period+1). Returns nil if there isn't
// enough history.
func EMASeries(klines []Kline, period int) []float64 {
	if len(klines) < period {
		return nil
	}
	k := 2.0 / (float64(period) + 1.0)
	vals := make([]float64, len(klines))
	vals[0] = klines[0].Close
	for i := 1; i < len(klines); i++ {
		vals[i] = klines[i].Close*k + vals[i-1]*(1-k)
	}
	return vals
}

func absF(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

func max3(a, b, c float64) float64 {
	m := a
	if b > m {
		m = b
	}
	if c > m {
		m = c
	}
	return m
}
