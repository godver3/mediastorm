package handlers

import (
	"encoding/json"
	"net/http"
	"strings"

	"novastream/config"
	"novastream/services/publicmetadb"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

type publicMetaDBAccountView struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (h *AdminUIHandler) publicMetaDBAccount(r *http.Request, settings config.Settings, id string) *config.PublicMetaDBAccount {
	account := settings.PublicMetaDB.GetAccountByID(id)
	if account == nil {
		return nil
	}
	isAdmin, ownerID, _, _ := h.getPageRoleInfo(r)
	if !isAdmin && account.OwnerAccountID != ownerID {
		return nil
	}
	return account
}

func (h *AdminUIHandler) ListPublicMetaDBAccounts(w http.ResponseWriter, r *http.Request) {
	settings, err := h.configManager.Load()
	if err != nil {
		http.Error(w, "Settings unavailable", 500)
		return
	}
	out := make([]publicMetaDBAccountView, 0)
	for _, account := range settings.PublicMetaDB.Accounts {
		if h.publicMetaDBAccount(r, settings, account.ID) != nil {
			out = append(out, publicMetaDBAccountView{account.ID, account.Name})
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func (h *AdminUIHandler) CreatePublicMetaDBAccount(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name   string `json:"name"`
		APIKey string `json:"apiKey"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil || strings.TrimSpace(body.APIKey) == "" {
		http.Error(w, "API key required", 400)
		return
	}
	client := &publicmetadb.Client{}
	if _, err := client.Lists(r.Context(), strings.TrimSpace(body.APIKey)); err != nil {
		http.Error(w, "PublicMetaDB API key could not be validated", 400)
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		name = "PublicMetaDB"
	}
	account := config.PublicMetaDBAccount{ID: uuid.NewString(), Name: name, APIKey: strings.TrimSpace(body.APIKey)}
	if isAdmin, ownerID, _, _ := h.getPageRoleInfo(r); !isAdmin {
		account.OwnerAccountID = ownerID
	}
	if !updateSettingsOr(h.configManager, func(settings *config.Settings) error {
		settings.PublicMetaDB.Accounts = append(settings.PublicMetaDB.Accounts, account)
		return nil
	}, func(error) { http.Error(w, "Settings unavailable", 500) },
		func(error) { http.Error(w, "Failed to save settings", 500) }) {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(publicMetaDBAccountView{account.ID, account.Name})
}

func (h *AdminUIHandler) DeletePublicMetaDBAccount(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["accountID"]
	if !updateSettingsOr(h.configManager, func(settings *config.Settings) error {
		if h.publicMetaDBAccount(r, *settings, id) == nil {
			http.Error(w, "Account not found", 404)
			return errSettingsUpdateAborted
		}
		accounts := settings.PublicMetaDB.Accounts[:0]
		for _, account := range settings.PublicMetaDB.Accounts {
			if account.ID != id {
				accounts = append(accounts, account)
			}
		}
		settings.PublicMetaDB.Accounts = accounts
		return nil
	}, func(error) { http.Error(w, "Settings unavailable", 500) },
		func(error) { http.Error(w, "Failed to save settings", 500) }) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *AdminUIHandler) PublicMetaDBLists(w http.ResponseWriter, r *http.Request) {
	settings, err := h.configManager.Load()
	if err != nil {
		http.Error(w, "Settings unavailable", 500)
		return
	}
	account := h.publicMetaDBAccount(r, settings, mux.Vars(r)["accountID"])
	if account == nil {
		http.Error(w, "Account not found", 404)
		return
	}
	lists, err := (&publicmetadb.Client{}).Lists(r.Context(), account.APIKey)
	if err != nil {
		http.Error(w, "PublicMetaDB lists unavailable", 502)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"lists": lists})
}
