# 🐉 Cacing Jadi Naga — Go / Bybit v5 REST Edition

M1 support/resistance scalper for **1000BONK/USDT:USDT** linear perpetual on
Bybit, written in pure Go against the **raw Bybit v5 REST API** — no CCXT,
no third-party dependencies, just the standard library. Compiles to a
single static binary.

---

## ⚠️ RISK DISCLAIMER

This build runs **100% of your free USDT balance as margin, every trade**
(`POSITION_PCT=1.0`), at **30x leverage**, with **deliberately loose entry
filters** (EMA and order-book confirmation gates are soft, tuned to fire
often rather than rarely). This is the aggressive end of the spectrum on
purpose, per spec.

- A hard stop-loss (ATR-based) is attached to every position **on the
  exchange itself** the instant it opens, and a trailing stop follows
  price favorably. Both live on Bybit's servers via the native
  `/v5/position/trading-stop` endpoint — they keep working even if this
  bot's VPS disconnects, crashes, or reboots.
- Even so, 100% margin at 30x leaves a thin buffer to liquidation (roughly
  ~1/30 ≈ 3.3% adverse move). A stop-loss reduces this but does not
  eliminate liquidation risk during extreme volatility or slippage.
- Only trade money you are fully prepared to lose.
- This is not financial advice. No warranty of any kind.

---

## 📦 What's in this repo

| File | Purpose |
|---|---|
| `main.go` | Entry point: startup, instrument info, leverage, the hunting loop |
| `config.go` | `.env` / environment variable loading |
| `bybit.go` | Bybit v5 REST client: HMAC signing, retry/backoff, typed endpoints |
| `indicators.go` | ATR and EMA calculations |
| `strategy.go` | Support/resistance setup detection + EMA/order-book confirmation |
| `position.go` | Position sizing (all-in compounding) and order execution |
| `manage.go` | Trade management: native hard SL + trailing stop, TP, reversal bail-out |
| `util.go` | Tiny timestamped logger |
| `go.mod` | Module definition — zero third-party dependencies |
| `.env.example` | Config template |
| `.gitignore` | Keeps `.env` and the compiled binary out of git |

---

## 🖥️ Installation & build

### VPS (Ubuntu/Debian/AlmaLinux)

```bash
# 1. Install Go (if not already installed) - check https://go.dev/dl/ for
#    the current version; example for a recent stable release:
curl -LO https://go.dev/dl/go1.22.linux-amd64.tar.gz
sudo rm -rf /usr/local/go && sudo tar -C /usr/local -xzf go1.22.linux-amd64.tar.gz
echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.bashrc
source ~/.bashrc
go version

# 2. Clone and build
git clone https://github.com/YOUR_USERNAME/YOUR_REPO.git naga-bot-go
cd naga-bot-go
go build -o naga-bot-go .

# 3. Configure
cp .env.example .env
nano .env   # fill in your API key/secret

# 4. Run
./naga-bot-go
```

Keep it running 24/7 with `screen`/`tmux`, or as a `systemd` service:

```ini
# /etc/systemd/system/naga-bot-go.service
[Unit]
Description=Cacing Jadi Naga Go Scalper
After=network.target

[Service]
Type=simple
WorkingDirectory=/home/YOUR_USERNAME/naga-bot-go
ExecStart=/home/YOUR_USERNAME/naga-bot-go/naga-bot-go
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl daemon-reload
sudo systemctl enable naga-bot-go
sudo systemctl start naga-bot-go
journalctl -u naga-bot-go -f
```

**Since this is a compiled binary**, `.env` must sit in the same directory
you run it from (or export the variables directly in the systemd unit /
shell environment instead).

---

## ⚙️ Configuration

See `.env.example` for the full list with comments. The most important
ones:

| Variable | Default | Meaning |
|---|---|---|
| `SYMBOL` | `1000BONKUSDT` | Raw Bybit v5 symbol — **no slashes/colons** |
| `LEVERAGE` | `30` | Clipped automatically to the symbol's real exchange max at startup |
| `POSITION_PCT` | `1.0` | Fraction of free balance used as margin — 1.0 = all-in |
| `SL_ATR_MULT` / `TRAIL_ATR_MULT` | `0.80` / `0.55` | Initial stop distance and trailing distance, in ATR multiples |
| `TP_PCT` | `0.009` | Take-profit target (0.9%) |
| `EMA_SLOPE_TOLERANCE` | `0.002` | How much against-trend EMA drift is tolerated before blocking a signal |
| `ORDERBOOK_ENTRY_RATIO` | `1.01` | How much the book must favor your direction to confirm entry (barely, by design) |
| `ORDERBOOK_FLIP_RATIO` | `1.7` | How hard the book must flip against an open position to trigger the bail-out |

**Strongly recommended:** set `USE_TESTNET=true` first and confirm on
Bybit's testnet (generate separate API keys at
[testnet.bybit.com](https://testnet.bybit.com)) that leverage, sizing, the
native stop-loss, and the trailing stop all behave as expected — *before*
pointing this at real funds.

---

## 🧠 How it differs from the Python/CCXT version

- **Direct REST calls, no CCXT.** Every request is hand-signed per Bybit's
  v5 HMAC spec (`bybit.go`), so there's no abstraction layer translating
  symbols/params — `SYMBOL` must be the exact Bybit ticker
  (`1000BONKUSDT`), not a CCXT-unified string.
- **Native trailing stop.** Instead of polling and canceling/re-placing a
  conditional order (as the Python version does), this build calls
  `/v5/position/trading-stop` with a new `stopLoss` price whenever the
  trail should move. Between updates, Bybit's own engine keeps the
  position protected — this is more resilient to a VPS network drop.
- **Zero dependencies.** Only the Go standard library is used, so
  `go build` produces a single static binary with nothing to `go mod
  download` on a fresh VPS.
- **Panic-recovering loop.** Every cycle of the hunting loop runs inside a
  `recover()` wrapper (`main.go`), so an unexpected bug logs a stack trace
  and the process keeps running instead of crashing — the Go equivalent of
  the Python version's catch-all exception handling.

---

## 🛑 Final Word

This build was NOT compiled/tested in a live Go toolchain during
generation (no Go compiler was available in the authoring environment) —
it was written and manually reviewed for correctness, but please run
`go build -o naga-bot-go .` yourself and check for any compiler errors
before deploying, and test on Bybit testnet before using real funds.

🐉
