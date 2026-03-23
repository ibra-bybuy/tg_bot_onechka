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
	"unicode"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/joho/godotenv"
	_ "modernc.org/sqlite"

	"tg_bot_anechka/internal/config"
)

const recentMessagesLimit = 1000

var giveDirectionPrefixes = []string{"выда", "выдам", "выдать", "выдаю"}
var receiveDirectionPrefixes = []string{"прим", "прием", "принима", "приемк"}
var queryStopWords = map[string]struct{}{
	"запрос":  {},
	"ищу":     {},
	"найти":   {},
	"контакт": {},
	"контакты": {},
	"от":      {},
	"до":      {},
	"на":      {},
	"в":       {},
	"по":      {},
	"и":       {},
	"или":     {},
	"eur":     {},
	"usd":     {},
	"rub":     {},
	"k":       {},
	"к":       {},
	"кк":      {},
	"тыс":     {},
	"тысяч":   {},
	"млн":     {},
	"млрд":    {},
	"евро":    {},
	"доллар":  {},
	"доллара": {},
	"долларов": {},
	"руб":     {},
	"рубль":   {},
	"рубля":   {},
	"рублей":  {},
}

type searchRequest struct {
	Raw       string
	Direction string
	Locations []string
}

type storedMessage struct {
	MessageID int
	FromID    int64
	Username  string
	Text      string
	CreatedAt time.Time
}

type userMatch struct {
	FromID    int64
	Username  string
	Text      string
	CreatedAt time.Time
}

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
	if msg == nil || msg.Chat == nil {
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

	text := extractMessageText(msg)

	if request, ok := extractSearchRequest(msg, bot.Self.UserName); ok {
		if len(request.Locations) == 0 {
			reply := tgbotapi.NewMessage(msg.Chat.ID, "Напишите запрос с городом, например: #Запрос Майами или #Запрос Выдать Сочи")
			reply.ReplyToMessageID = msg.MessageID
			_, _ = bot.Send(reply)
			return
		}

		matches, err := findMatchingUsers(db, msg.Chat.ID, request, recentMessagesLimit)
		if err != nil {
			reply := tgbotapi.NewMessage(msg.Chat.ID, "Не удалось выполнить поиск. Попробуйте еще раз чуть позже.")
			reply.ReplyToMessageID = msg.MessageID
			_, _ = bot.Send(reply)
			log.Printf("search error: %v", err)
			return
		}

		if len(matches) == 0 {
			reply := tgbotapi.NewMessage(msg.Chat.ID, "Ничего не найдено по этому запросу.")
			reply.ReplyToMessageID = msg.MessageID
			_, _ = bot.Send(reply)
			return
		}

		var b strings.Builder
		b.WriteString("Подходящие контакты:\n")
		for _, match := range matches {
			when := match.CreatedAt.Format("2006-01-02 15:04")
			b.WriteString(fmt.Sprintf("- %s %s", formatUserLink(match.FromID, match.Username), html.EscapeString(trimForPreview(match.Text, 90))))
			b.WriteString(fmt.Sprintf(" (%s)\n", when))
		}

		reply := tgbotapi.NewMessage(msg.Chat.ID, strings.TrimSpace(b.String()))
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

func findMatchingUsers(db *sql.DB, chatID int64, request searchRequest, limit int) ([]userMatch, error) {
	messages, err := fetchRecentMessages(db, chatID, limit)
	if err != nil {
		return nil, err
	}

	matches := make([]userMatch, 0, 8)
	seen := make(map[string]struct{})
	for _, msg := range messages {
		if !messageMatchesRequest(msg.Text, request) {
			continue
		}

		key := userKey(msg.FromID, msg.Username)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}

		matches = append(matches, userMatch{
			FromID:    msg.FromID,
			Username:  msg.Username,
			Text:      msg.Text,
			CreatedAt: msg.CreatedAt,
		})
	}

	return matches, nil
}

func fetchRecentMessages(db *sql.DB, chatID int64, limit int) ([]storedMessage, error) {
	rows, err := db.Query(`
		SELECT message_id, from_id, username, text, created_at
		FROM messages
		WHERE chat_id = ?
		ORDER BY created_at DESC
		LIMIT ?
	`, chatID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	results := make([]storedMessage, 0, limit)
	for rows.Next() {
		var item storedMessage
		var createdAt int64
		if err := rows.Scan(&item.MessageID, &item.FromID, &item.Username, &item.Text, &createdAt); err != nil {
			return nil, err
		}
		item.CreatedAt = time.Unix(createdAt, 0)
		results = append(results, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

func messageMatchesRequest(text string, request searchRequest) bool {
	normalized := normalizeText(text)
	if normalized == "" {
		return false
	}

	if isSearchCommand(normalized) {
		return false
	}

	if request.Direction != "" && !matchesDirection(normalized, request.Direction) {
		return false
	}

	for _, location := range request.Locations {
		if containsPhrase(normalized, location) {
			return true
		}
	}

	return false
}

func extractSearchRequest(msg *tgbotapi.Message, botUsername string) (searchRequest, bool) {
	if msg == nil {
		return searchRequest{}, false
	}

	if query, ok := extractHashtagQuery(msg); ok {
		return buildSearchRequest(query), true
	}

	query, mentioned := extractMentionQuery(msg, botUsername)
	if !mentioned {
		return searchRequest{}, false
	}
	return buildSearchRequest(query), true
}

func extractHashtagQuery(msg *tgbotapi.Message) (string, bool) {
	raw := extractMessageText(msg)
	if raw == "" {
		return "", false
	}

	normalized := normalizeText(raw)
	if normalized == "" {
		return "", false
	}

	marker := "запрос"
	idx := strings.Index(normalized, marker)
	if idx == -1 {
		return "", false
	}

	if !strings.Contains(strings.ToLower(raw), "#запрос") && !strings.HasPrefix(normalized, marker) {
		return "", false
	}

	query := strings.TrimSpace(strings.TrimPrefix(normalized[idx:], marker))
	return query, true
}

func buildSearchRequest(query string) searchRequest {
	normalized := normalizeText(query)
	return searchRequest{
		Raw:       normalized,
		Direction: detectDirection(normalized),
		Locations: extractLocationPhrases(normalized),
	}
}

func detectDirection(text string) string {
	if hasAnyPrefix(text, giveDirectionPrefixes) {
		return "give"
	}
	if hasAnyPrefix(text, receiveDirectionPrefixes) {
		return "receive"
	}
	return ""
}

func matchesDirection(text, direction string) bool {
	switch direction {
	case "give":
		return hasAnyPrefix(text, giveDirectionPrefixes)
	case "receive":
		return hasAnyPrefix(text, receiveDirectionPrefixes)
	default:
		return true
	}
}

func hasAnyPrefix(text string, prefixes []string) bool {
	for _, token := range strings.Fields(text) {
		for _, prefix := range prefixes {
			if strings.HasPrefix(token, prefix) {
				return true
			}
		}
	}
	return false
}

func extractLocationPhrases(text string) []string {
	tokens := strings.Fields(text)
	filtered := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if shouldSkipQueryToken(token) {
			continue
		}
		filtered = append(filtered, token)
	}

	if len(filtered) == 0 {
		return nil
	}

	phrases := make([]string, 0, len(filtered))
	joined := strings.Join(filtered, " ")
	if joined != "" {
		phrases = append(phrases, joined)
	}
	for _, token := range filtered {
		if len([]rune(token)) >= 3 {
			phrases = append(phrases, token)
		}
	}
	return uniqueStrings(phrases)
}

func shouldSkipQueryToken(token string) bool {
	if token == "" {
		return true
	}
	if _, ok := queryStopWords[token]; ok {
		return true
	}
	if hasAnyPrefix(token, giveDirectionPrefixes) || hasAnyPrefix(token, receiveDirectionPrefixes) {
		return true
	}
	for _, r := range token {
		if unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

func containsPhrase(text, phrase string) bool {
	if phrase == "" {
		return false
	}
	haystack := " " + text + " "
	needle := " " + phrase + " "
	return strings.Contains(haystack, needle)
}

func isSearchCommand(normalized string) bool {
	return strings.HasPrefix(normalized, "запрос ") || normalized == "запрос"
}

func extractMessageText(msg *tgbotapi.Message) string {
	if msg == nil {
		return ""
	}
	if msg.Text != "" {
		return msg.Text
	}
	return msg.Caption
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

func normalizeText(text string) string {
	text = strings.ToLower(strings.ReplaceAll(text, "ё", "е"))
	var b strings.Builder
	b.Grow(len(text))
	lastSpace := true
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			lastSpace = false
			continue
		}
		if !lastSpace {
			b.WriteByte(' ')
			lastSpace = true
		}
	}
	return strings.TrimSpace(b.String())
}

func uniqueStrings(items []string) []string {
	seen := make(map[string]struct{}, len(items))
	result := make([]string, 0, len(items))
	for _, item := range items {
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		result = append(result, item)
	}
	return result
}

func userKey(fromID int64, username string) string {
	if fromID != 0 {
		return fmt.Sprintf("id:%d", fromID)
	}
	if username != "" {
		return "username:" + strings.ToLower(strings.TrimPrefix(username, "@"))
	}
	return ""
}
