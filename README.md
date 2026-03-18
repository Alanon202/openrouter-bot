<h1 align="center">
    <img src="img/logo.png" width="220" />
    <div>
    OpenRouter
    <br>
    Bot (Refactored)
    </div>
</h1>

<h4 align="center">
    <strong>English (🇺🇸)</strong> | <a href="README_RU.md">Русский (🇷🇺)</a>
</h4>

This project allows you to launch your Telegram bot in a few minutes to communicate with free and paid AI models via [OpenRouter](https://openrouter.ai).

> [!IMPORTANT]
> This repository is a significant refactor and improvement of the original bot, migrated to the modern `go-telegram/bot` library. It is designed for high reliability, performance, and advanced Telegram features like Forum Topics.

### Key Improvements & Advantages

- **Modern Telegram Engine**: Migrated from `telegram-bot-api` to `go-telegram/bot` for better performance and support for the latest Telegram API features.
- **Forum/Thread Support**: Full support for Telegram Forum Topics (`MessageThreadID`), allowing the bot to work seamlessly in organized group chats.
- **High Concurrency Safety**: Completely refactored state management with fine-grained locking, preventing race conditions even with many simultaneous users.
- **Optimized for Docker**:
  - Reduced resource footprint through singleton manager patterns.
  - Optional `.env` support.
  - Improved graceful shutdown and signal handling.
- **Enhanced Reliability**:
  - Implementation of `SafeEdit` to prevent message truncation on Markdown errors.
  - Automatic API timeouts (5m) to prevent hanging goroutines.
  - Update deduplication to prevent "double-bubble" AI responses.
- **Smooth UX**:
  - Throttled streaming updates (800ms) for a fluid visual experience without hitting rate limits.
  - Faster animation dots (400ms) for better interactivity.
  - Improved error handling and automatic plain-text fallback.

## Preparation

- Register with [OpenRouter](https://openrouter.ai) and get an [API key](https://openrouter.ai/settings/keys).
- Create your Telegram bot using [@BotFather](https://telegram.me/BotFather) and get its API token.
- Get your telegram id using [@getmyid_bot](https://t.me/getmyid_bot).

## Installation

### Running in Docker

- Create a working directory:

```bash
mkdir openrouter-bot
cd openrouter-bot
```

- Create `.env` file (or provide environment variables directly):

```bash
# OpenRouter api key
API_KEY=
# Free modeles: https://openrouter.ai/models?max_price=0
MODEL=deepseek/deepseek-r1:free
# Telegram api key
TELEGRAM_BOT_TOKEN=
# Your Telegram id
ADMIN_IDS=
# List of users to access the bot, separated by commas
ALLOWED_USER_IDS=
# Disable guest access (enabled by default)
GUEST_BUDGET=0
# Language used for bot responses (supported: EN/RU)
LANG=EN
```

- Run using Docker Compose:

```bash
docker-compose up -d --build
```

## Build

```bash
go build -o openrouter-bot .
```
