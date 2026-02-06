package main

import (
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/joho/godotenv"

	"tg_bot_anechka/internal/config"
)

func main() {
	_ = godotenv.Load()

	settings := config.Load()
	if settings.BotToken == "" {
		log.Fatal("BOT_TOKEN is not set. Check .env")
	}

	client := &http.Client{}
	if settings.ProxyURL != "" {
		u, err := url.Parse(settings.ProxyURL)
		if err != nil {
			log.Fatalf("invalid PROXY_URL: %v", err)
		}
		client.Transport = &http.Transport{
			Proxy: http.ProxyURL(u),
		}
	}

	bot, err := tgbotapi.NewBotAPIWithClient(settings.BotToken, tgbotapi.APIEndpoint, client)
	if err != nil {
		log.Fatalf("failed to create bot: %v", err)
	}

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	updates := bot.GetUpdatesChan(u)

	log.Println("Bot is running. Press Ctrl+C to stop.")

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)

	for {
		select {
		case update := <-updates:
			handleUpdate(bot, update, settings)
		case <-sig:
			log.Println("Shutting down...")
			return
		}
	}
}

func handleUpdate(bot *tgbotapi.BotAPI, update tgbotapi.Update, settings config.Settings) {
	msg := update.Message
	if msg == nil {
		msg = update.EditedMessage
	}
	if msg == nil {
		return
	}

	if msg.Chat == nil {
		return
	}

	if msg.Chat.Type != "group" && msg.Chat.Type != "supergroup" {
		return
	}

	if len(settings.AllowedGroupIDs) > 0 {
		if _, ok := settings.AllowedGroupIDs[msg.Chat.ID]; !ok {
			return
		}
	}

	username := "(no username)"
	if msg.From != nil && msg.From.UserName != "" {
		username = "@" + msg.From.UserName
	}

	text := msg.Text
	if text == "" {
		text = msg.Caption
	}

	bot.Send(tgbotapi.NewMessage(msg.Chat.ID, fmt.Sprintf("%s написал: %s", username, text)))

	fmt.Printf("[%d] %s: %s\n", msg.Chat.ID, username, text)
}
