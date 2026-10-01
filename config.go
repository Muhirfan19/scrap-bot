package main

// ============================================================================
// CONFIG - loads settings from a ".env" file (if present) and the real OS
// environment. OS environment always wins, so you can override .env values
// with `SOME_VAR=x ./naga-bot-go` at launch time without editing the file.
// ============================================================================

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

// Config holds every tunable knob for the bot. Field comments describe what
// each one does; defaults mirror the aggressive "Cacing Jadi Naga" spec.
type Config struct {
	// --- Bybit API credentials & environment ---
	APIKey     string
	APISecret  string
	UseTestnet bool

	// --- Symbol & leverage ---
	Symbol   string // raw Bybit v5 symbol, e.g. "1000BONKUSDT" (no slashes/colons)
	Category string // "linear" for USDT perpetuals
	Leverage int    // requested leverage; clipped to the symbol's real exchange max at startup

	// --- Position sizing ("Cacing Jadi Naga" - all-in compounding) ---
	PositionPct    float64 // fraction of free USDT balance used as margin per trade (1.0 = 100%)
	MinFreeBalance float64 // absolute floor in USDT - refuse to trade below this to avoid API errors on dust amounts

	// --- Entry setup (support/resistance pullback) ---
	Lookback        int     // candles used to compute rolling support/resistance
	ATRPeriod       int     // candles used for ATR
	ZoneATRMult     float64 // how wide the support/resistance "zone" is, as a multiple of ATR
	MinWickRatio    float64 // minimum wick-to-range ratio to count as a rejection candle
	MinVolumeRatio  float64 // minimum current volume vs recent average to confirm real participation
	MinScore        float64 // minimum composite score to allow a signal through

	// --- EMA trend filter (soft filter, not a hard directional gate) ---
	EMAPeriod          int
	EMASlopeLookback   int     // candles back to compare EMA slope against
	EMASlopeTolerance  float64 // allowed against-trend EMA drift (fraction, e.g. 0.002 = 0.2%) before blocking
	RequireEMAConfirm  bool

	// --- Order book entry confirmation (soft filter) ---
	RequireOrderbookEntryConfirm bool
	OrderbookEntryRatio          float64 // how much bid/ask volume must favor our side to confirm (1.01 = barely)
	OrderbookDepth               int     // levels summed on each side of the book

	// --- Entry execution ---
	EntryOrderType string // "market" (instant, taker) - this bot is market-only per spec

	// --- Exit management ---
	TPPct           float64 // take-profit target as a fraction of entry price
	SLATRMult       float64 // initial hard stop-loss distance, as a multiple of ATR
	BETriggerPct    float64 // profit fraction at which trailing is considered "armed" (for logging/threshold use)
	TrailATRMult    float64 // trailing stop distance behind the best price, as a multiple of ATR
	TrailMinStepATR float64 // minimum improvement (in ATR units) before amending the trailing stop again

	// --- Order-book reversal bail-out (second layer of protection) ---
	OrderbookFlipRatio float64 // how hard the book must flip against an open position to trigger an emergency exit

	// --- Loop timing ---
	ScanIntervalMs   int // how often to scan for a new signal while flat
	ManageIntervalMs int // how often to poll price/orderbook while a position is open

	// --- HTTP / rate limiting ---
	RecvWindowMs    int64 // Bybit recv_window for signed requests
	MaxRetries      int   // max retries per HTTP call on network/429 errors
	BaseBackoffMs   int   // base delay for exponential backoff
	MaxBackoffMs    int   // cap for exponential backoff delay
}

// loadDotEnv reads a simple KEY=VALUE ".env" file into the process
// environment, without overwriting any variable that's already set (so real
// environment variables always take priority over the file). Missing file
// is not an error - the bot can run purely off exported env vars too.
func loadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return // no .env file - that's fine, rely on real env vars
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		// Strip surrounding quotes if present, e.g. KEY="value"
		val = strings.Trim(val, `"'`)
		if _, exists := os.LookupEnv(key); !exists {
			os.Setenv(key, val)
		}
	}
}

// getEnvStr / getEnvBool / getEnvInt / getEnvFloat are small helpers that
// read an env var with a fallback default, so every setting in Config below
// is overridable from .env or the real environment without code changes.
func getEnvStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getEnvBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func getEnvInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func getEnvFloat(key string, def float64) float64 {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return def
	}
	return f
}

// LoadConfig assembles the full Config from .env + environment, applying
// the aggressive defaults from the spec wherever a var isn't explicitly set.
func LoadConfig() Config {
	loadDotEnv(".env")

	return Config{
		APIKey:     getEnvStr("BYBIT_API_KEY", ""),
		APISecret:  getEnvStr("BYBIT_API_SECRET", ""),
		UseTestnet: getEnvBool("USE_TESTNET", true),

		Symbol:   getEnvStr("SYMBOL", "1000BONKUSDT"),
		Category: "linear",
		Leverage: getEnvInt("LEVERAGE", 30),

		PositionPct:    getEnvFloat("POSITION_PCT", 1.0),
		MinFreeBalance: getEnvFloat("MIN_FREE_BALANCE_USDT", 1.0),

		Lookback:       getEnvInt("LOOKBACK", 20),
		ATRPeriod:      getEnvInt("ATR_PERIOD", 14),
		ZoneATRMult:    getEnvFloat("ZONE_ATR", 0.6),
		MinWickRatio:   getEnvFloat("MIN_WICK_RATIO", 0.15),
		MinVolumeRatio: getEnvFloat("MIN_VOLUME_RATIO", 0.5),
		MinScore:       getEnvFloat("MIN_SCORE", 0.9),

		EMAPeriod:         getEnvInt("EMA_PERIOD", 9),
		EMASlopeLookback:  getEnvInt("EMA_SLOPE_LOOKBACK", 3),
		EMASlopeTolerance: getEnvFloat("EMA_SLOPE_TOLERANCE", 0.002),
		RequireEMAConfirm: getEnvBool("REQUIRE_EMA_CONFIRM", true),

		RequireOrderbookEntryConfirm: getEnvBool("REQUIRE_ORDERBOOK_ENTRY_CONFIRM", true),
		OrderbookEntryRatio:          getEnvFloat("ORDERBOOK_ENTRY_RATIO", 1.01),
		OrderbookDepth:               getEnvInt("ORDERBOOK_DEPTH", 10),

		EntryOrderType: getEnvStr("ENTRY_ORDER_TYPE", "market"),

		TPPct:           getEnvFloat("TP_PCT", 0.009),
		SLATRMult:       getEnvFloat("SL_ATR_MULT", 0.80),
		BETriggerPct:    getEnvFloat("BE_TRIGGER_PCT", 0.0035),
		TrailATRMult:    getEnvFloat("TRAIL_ATR_MULT", 0.55),
		TrailMinStepATR: getEnvFloat("TRAIL_MIN_STEP_ATR", 0.15),

		OrderbookFlipRatio: getEnvFloat("ORDERBOOK_FLIP_RATIO", 1.7),

		ScanIntervalMs:   getEnvInt("SCAN_INTERVAL_MS", 500),
		ManageIntervalMs: getEnvInt("MANAGE_INTERVAL_MS", 500),

		RecvWindowMs:  int64(getEnvInt("RECV_WINDOW_MS", 5000)),
		MaxRetries:    getEnvInt("MAX_RETRIES", 5),
		BaseBackoffMs: getEnvInt("BASE_BACKOFF_MS", 400),
		MaxBackoffMs:  getEnvInt("MAX_BACKOFF_MS", 8000),
	}
}
