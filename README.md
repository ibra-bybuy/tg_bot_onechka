# Telegram Group Listener Bot (Go)

Minimal Telegram bot scaffold that reads group messages.

## Setup

1) Create and activate a Go environment.
2) Download deps: `go mod tidy`
3) Fill `.env` with your bot token and optional group IDs.

`ALLOWED_GROUP_IDS` is a comma-separated list of numeric chat IDs. Leave empty to allow all groups the bot is in.
`PROXY_URL` is optional. Example: `socks5://127.0.0.1:1080` or `http://127.0.0.1:1080`.

## Run

`go run ./cmd/bot`

## Notes

- The bot logs group messages to stdout.
- It ignores private chats and channels by default.
