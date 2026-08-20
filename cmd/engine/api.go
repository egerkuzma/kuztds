package main

import (
	"encoding/base64"
	"encoding/json"

	"github.com/egerkuzma/kuztds/internal/security"
)

// apiRequest — data from an API client as base64(JSON).
type apiRequest struct {
	KeyAPI    string   `json:"key_api"`
	ID        string   `json:"id"`
	IP        string   `json:"ip"`
	Referer   string   `json:"referer"`
	UserAgent string   `json:"useragent"`
	SE        string   `json:"se"`
	Lang      string   `json:"lang"`
	Uniq      string   `json:"uniq"`
	Key       string   `json:"key"`
	Domain    string   `json:"domain"`
	Page      string   `json:"page"`
	CFCountry string   `json:"cf_country"`
	Pars      []string `json:"pars"`
}

// parseAPIRequest decodes base64(JSON) and validates the API key.
func parseAPIRequest(param, key string) (*apiRequest, bool) {
	raw, err := base64.StdEncoding.DecodeString(param)
	if err != nil {
		if raw, err = base64.URLEncoding.DecodeString(param); err != nil {
			return nil, false
		}
	}
	var req apiRequest
	if json.Unmarshal(raw, &req) != nil {
		return nil, false
	}
	// Constant-time compare: the key travels in a request parameter an
	// unauthenticated caller controls, so it is compared like every other
	// secret in this codebase (see security.EqualTokens).
	if key == "" || !security.EqualTokens(req.KeyAPI, key) {
		return nil, false
	}
	return &req, true
}
