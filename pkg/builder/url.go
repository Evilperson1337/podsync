package builder

import (
	"net/url"
	"strings"

	"github.com/pkg/errors"

	"github.com/mxpv/podsync/pkg/model"
)

func ParseURL(link string) (model.Info, error) {
	parsed, err := parseURL(link)
	if err != nil {
		return model.Info{}, err
	}

	info := model.Info{}

	host := normalizeHost(parsed.Host)

	if host == "youtu.be" {
		host = "youtube.com"
	}

	if strings.HasSuffix(host, "youtube.com") {
		kind, id, err := parseYoutubeURL(parsed)
		if err != nil {
			return model.Info{}, err
		}

		info.Provider = model.ProviderYoutube
		info.LinkType = kind
		info.ItemID = id

		return info, nil
	}

	if strings.HasSuffix(host, "vimeo.com") {
		kind, id, err := parseVimeoURL(parsed)
		if err != nil {
			return model.Info{}, err
		}

		info.Provider = model.ProviderVimeo
		info.LinkType = kind
		info.ItemID = id

		return info, nil
	}

	if strings.HasSuffix(host, "soundcloud.com") {
		kind, id, err := parseSoundcloudURL(parsed)
		if err != nil {
			return model.Info{}, err
		}

		info.Provider = model.ProviderSoundcloud
		info.LinkType = kind
		info.ItemID = id

		return info, nil
	}

	if strings.HasSuffix(host, "twitch.tv") {
		kind, id, err := parseTwitchURL(parsed)
		if err != nil {
			return model.Info{}, err
		}

		info.Provider = model.ProviderTwitch
		info.LinkType = kind
		info.ItemID = id

		return info, nil
	}

	if strings.HasSuffix(host, "rumble.com") {
		kind, id, err := parseRumbleURL(parsed)
		if err != nil {
			return model.Info{}, err
		}

		info.Provider = model.ProviderRumble
		info.LinkType = kind
		info.ItemID = id

		return info, nil
	}

	return model.Info{}, errors.New("unsupported URL host")
}

func parseURL(link string) (*url.URL, error) {
	if !strings.HasPrefix(link, "http") {
		link = "https://" + link
	}

	parsed, err := url.Parse(link)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to parse url: %s", link)
	}

	return parsed, nil
}

func normalizeHost(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	host = strings.TrimPrefix(host, "www.")
	host = strings.TrimPrefix(host, "m.")
	host = strings.TrimPrefix(host, "mobile.")
	return host
}

func pathParts(parsed *url.URL) []string {
	parts := strings.Split(parsed.EscapedPath(), "/")
	filtered := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			filtered = append(filtered, part)
		}
	}
	return filtered
}

func parseYoutubeURL(parsed *url.URL) (model.Type, string, error) {
	path := parsed.EscapedPath()
	parts := pathParts(parsed)
	if strings.Contains(path, "//") {
		return "", "", errors.New("invalid youtube link")
	}

	// https://www.youtube.com/playlist?list=PLCB9F975ECF01953C
	// https://www.youtube.com/watch?v=rbCbho7aLYw&list=PLMpEfaKcGjpWEgNtdnsvLX6LzQL0UC0EM
	if strings.HasPrefix(path, "/playlist") || strings.HasPrefix(path, "/watch") {
		kind := model.TypePlaylist

		id := parsed.Query().Get("list")
		if id != "" {
			return kind, id, nil
		}

		return "", "", errors.New("invalid playlist link")
	}

	// - https://www.youtube.com/channel/UC5XPnUk8Vvv_pWslhwom6Og
	// - https://www.youtube.com/channel/UCrlakW-ewUT8sOod6Wmzyow/videos
	if strings.HasPrefix(path, "/channel") {
		kind := model.TypeChannel
		if len(parts) < 2 {
			return "", "", errors.New("invalid youtube channel link")
		}

		id := parts[1]
		if id == "" {
			return "", "", errors.New("invalid id")
		}

		return kind, id, nil
	}

	// - https://www.youtube.com/user/fxigr1
	if strings.HasPrefix(path, "/user") {
		kind := model.TypeUser

		if len(parts) < 2 {
			return "", "", errors.New("invalid user link")
		}

		id := parts[1]
		if id == "" {
			return "", "", errors.New("invalid id")
		}

		return kind, id, nil
	}

	// - https://www.youtube.com/@username
	// - https://www.youtube.com/@username/videos
	if strings.HasPrefix(path, "/@") {
		kind := model.TypeHandle

		if len(parts) < 1 {
			return "", "", errors.New("invalid handle link")
		}

		handle := parts[0]
		if handle == "" || !strings.HasPrefix(handle, "@") {
			return "", "", errors.New("invalid handle format")
		}

		// Remove the @ prefix for storage
		id := strings.TrimPrefix(handle, "@")
		if id == "" {
			return "", "", errors.New("empty handle")
		}

		return kind, id, nil
	}

	return "", "", errors.New("unsupported link format")
}

func parseVimeoURL(parsed *url.URL) (model.Type, string, error) {
	parts := pathParts(parsed)
	if len(parts) < 1 {
		return "", "", errors.New("invalid vimeo link path")
	}

	var kind model.Type
	switch parts[0] {
	case "groups":
		kind = model.TypeGroup
	case "channels":
		kind = model.TypeChannel
	default:
		kind = model.TypeUser
	}

	if kind == model.TypeGroup || kind == model.TypeChannel {
		if len(parts) < 2 {
			return "", "", errors.New("invalid channel link")
		}

		id := parts[1]
		if id == "" {
			return "", "", errors.New("invalid id")
		}

		return kind, id, nil
	}

	if kind == model.TypeUser {
		id := parts[0]
		if id == "" {
			return "", "", errors.New("invalid id")
		}

		return kind, id, nil
	}

	return "", "", errors.New("unsupported link format")
}

func parseSoundcloudURL(parsed *url.URL) (model.Type, string, error) {
	parts := pathParts(parsed)
	if len(parts) < 3 {
		return "", "", errors.New("invald soundcloud link path")
	}

	var kind model.Type

	// - https://soundcloud.com/user/sets/example-set
	switch parts[1] {
	case "sets":
		kind = model.TypePlaylist
	default:
		return "", "", errors.New("invalid soundcloud url, missing sets")
	}

	id := parts[2]

	return kind, id, nil
}

func parseTwitchURL(parsed *url.URL) (model.Type, string, error) {
	// - https://www.twitch.tv/samueletienne
	path := parsed.EscapedPath()
	parts := pathParts(parsed)
	if path == "/" {
		return "", "", errors.New("invalid id")
	}
	if len(parts) != 1 {
		return "", "", errors.Errorf("invald twitch user path: %s", path)
	}

	kind := model.TypeUser

	id := parts[0]
	if id == "" {
		return "", "", errors.New("invalid id")
	}

	return kind, id, nil
}

func parseRumbleURL(parsed *url.URL) (model.Type, string, error) {
	path := strings.TrimSuffix(parsed.EscapedPath(), "/")
	if path == "" || path == "/" {
		return "", "", errors.New("invalid rumble link path")
	}

	parts := pathParts(parsed)
	if len(parts) < 2 {
		return "", "", errors.New("invalid rumble link path")
	}

	if parts[0] != "c" && parts[0] != "user" {
		return "", "", errors.New("invalid rumble link path")
	}

	id := parts[1]
	if id == "" {
		return "", "", errors.New("invalid rumble channel id")
	}

	if len(parts) >= 3 && parts[2] == "livestreams" {
		return model.TypeLivestreams, id, nil
	}

	return model.TypeChannel, id, nil
}
