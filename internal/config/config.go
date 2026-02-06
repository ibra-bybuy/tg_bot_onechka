package config

import (
    "os"
    "strconv"
    "strings"
)

type Settings struct {
    BotToken        string
    AllowedGroupIDs map[int64]struct{}
    ProxyURL        string
}

func ParseGroupIDs(raw string) map[int64]struct{} {
    ids := map[int64]struct{}{}
    if strings.TrimSpace(raw) == "" {
        return ids
    }

    for _, part := range strings.Split(raw, ",") {
        p := strings.TrimSpace(part)
        if p == "" {
            continue
        }
        if v, err := strconv.ParseInt(p, 10, 64); err == nil {
            ids[v] = struct{}{}
        }
    }

    return ids
}

func Load() Settings {
    return Settings{
        BotToken:        os.Getenv("BOT_TOKEN"),
        AllowedGroupIDs: ParseGroupIDs(os.Getenv("ALLOWED_GROUP_IDS")),
        ProxyURL:        os.Getenv("PROXY_URL"),
    }
}
