package main

import (
	"database/sql"
	"fmt"
	"html"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/joho/godotenv"
	_ "modernc.org/sqlite"

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

	db, err := sql.Open("sqlite", settings.MessageDBPath)
	if err != nil {
		log.Fatalf("failed to open message DB: %v", err)
	}
	defer db.Close()

	if err := initMessageStore(db); err != nil {
		log.Fatalf("failed to init message DB: %v", err)
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
			handleUpdate(bot, update, settings, db)
		case <-sig:
			log.Println("Shutting down...")
			return
		}
	}
}

func handleUpdate(bot *tgbotapi.BotAPI, update tgbotapi.Update, settings config.Settings, db *sql.DB) {
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

	username := ""
	if msg.From != nil && msg.From.UserName != "" {
		username = msg.From.UserName
	}

	text := msg.Text
	if text == "" {
		text = msg.Caption
	}

	query, mentioned := extractMentionQuery(msg, bot.Self.UserName)
	if mentioned {
		if query == "" {
			reply := tgbotapi.NewMessage(msg.Chat.ID, "Напишите город, пример. @"+bot.Self.UserName+" Москва")
			reply.ReplyToMessageID = msg.MessageID
			_, _ = bot.Send(reply)
			return
		}

		results, err := searchMessages(db, msg.Chat.ID, query, 5)
		if err != nil {
			reply := tgbotapi.NewMessage(msg.Chat.ID, "Не удалось найти. Пожалуйста, повторите попытку тоже позже.")
			reply.ReplyToMessageID = msg.MessageID
			_, _ = bot.Send(reply)
			log.Printf("search error: %v", err)
			return
		}

		if len(results) == 0 {
			reply := tgbotapi.NewMessage(msg.Chat.ID, "Не удалось найти.")
			reply.ReplyToMessageID = msg.MessageID
			_, _ = bot.Send(reply)
			return
		}

		var b strings.Builder
		b.WriteString("Реузльтаты:\n")
		for _, r := range results {
			when := r.CreatedAt.Format("2006-01-02 15:04")
			b.WriteString(fmt.Sprintf("- [%s] %s", when, formatUserLink(r.FromID, r.Username)))
		}

		reply := tgbotapi.NewMessage(msg.Chat.ID, b.String())
		reply.ParseMode = "HTML"
		reply.ReplyToMessageID = msg.MessageID
		_, _ = bot.Send(reply)
		return
	}

	if err := storeMessage(db, msg, username, text); err != nil {
		log.Printf("Не удалось сохранить сообщение: %v", err)
	}

	displayUser := "(no username)"
	if username != "" {
		displayUser = "@" + username
	}
	fmt.Printf("[%d] %s: %s\n", msg.Chat.ID, displayUser, text)
}

type searchResult struct {
	MessageID int
	FromID    int64
	Username  string
	Text      string
	CreatedAt time.Time
}

func initMessageStore(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS messages (
			chat_id INTEGER NOT NULL,
			message_id INTEGER NOT NULL,
			from_id INTEGER,
			username TEXT,
			text TEXT,
			created_at INTEGER NOT NULL,
			PRIMARY KEY (chat_id, message_id)
		);
		CREATE INDEX IF NOT EXISTS idx_messages_chat_time ON messages(chat_id, created_at DESC);
		CREATE INDEX IF NOT EXISTS idx_messages_chat_text ON messages(chat_id, text);
	`)
	return err
}

func storeMessage(db *sql.DB, msg *tgbotapi.Message, username, text string) error {
	fromID := int64(0)
	if msg.From != nil {
		fromID = int64(msg.From.ID)
	}

	createdAt := int64(msg.Date)
	_, err := db.Exec(`
		INSERT OR REPLACE INTO messages
			(chat_id, message_id, from_id, username, text, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, msg.Chat.ID, msg.MessageID, fromID, username, text, createdAt)
	return err
}

func searchMessages(db *sql.DB, chatID int64, query string, limit int) ([]searchResult, error) {
	like := "%" + query + "%"
	rows, err := db.Query(`
		SELECT message_id, from_id, username, text, created_at
		FROM messages
		WHERE chat_id = ?
			AND text LIKE ?
			AND text NOT LIKE '/search%'
		ORDER BY created_at DESC
		LIMIT ?
	`, chatID, like, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	results := make([]searchResult, 0, limit)
	for rows.Next() {
		var r searchResult
		var createdAt int64
		if err := rows.Scan(&r.MessageID, &r.FromID, &r.Username, &r.Text, &createdAt); err != nil {
			return nil, err
		}
		r.CreatedAt = time.Unix(createdAt, 0)
		results = append(results, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

func trimForPreview(text string, max int) string {
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	return string(runes[:max]) + "..."
}

func formatUserLink(fromID int64, username string) string {
	if username == "(no username)" {
		username = ""
	}
	clean := strings.TrimPrefix(username, "@")
	label := "(no username)"
	if clean != "" {
		label = "@" + clean
	}
	escapedLabel := html.EscapeString(label)
	if fromID != 0 {
		return fmt.Sprintf(`<a href="tg://user?id=%d">%s</a>`, fromID, escapedLabel)
	}
	if clean != "" {
		return fmt.Sprintf(`<a href="https://t.me/%s">%s</a>`, html.EscapeString(clean), escapedLabel)
	}
	return escapedLabel
}

func extractMentionQuery(msg *tgbotapi.Message, botUsername string) (string, bool) {
	if msg == nil || botUsername == "" {
		return "", false
	}

	raw := msg.Text
	entities := msg.Entities
	if raw == "" {
		raw = msg.Caption
		entities = msg.CaptionEntities
	}
	if raw == "" || len(entities) == 0 {
		return "", false
	}

	botTag := "@" + botUsername
	mentioned := false
	cleaned := raw

	for i := len(entities) - 1; i >= 0; i-- {
		e := entities[i]
		if e.Type != "mention" {
			continue
		}
		start := e.Offset
		end := e.Offset + e.Length
		runes := []rune(cleaned)
		if start < 0 || end > len(runes) || start >= end {
			continue
		}
		token := string(runes[start:end])
		if !strings.EqualFold(token, botTag) {
			continue
		}
		mentioned = true
		cleaned = string(runes[:start]) + string(runes[end:])
	}

	if !mentioned {
		return "", false
	}

	return strings.TrimSpace(cleaned), true
}
