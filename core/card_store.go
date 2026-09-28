package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type cardStore struct {
	base, token, owner string
	client             *http.Client
}

func newCardStore(owner string) (*cardStore, error) {
	base, token := strings.TrimRight(os.Getenv("CC_CARD_STORE_URL"), "/"), os.Getenv("CC_CARD_STORE_TOKEN")
	if base == "" && token == "" {
		return nil, nil
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || token == "" {
		return nil, fmt.Errorf("invalid card store configuration")
	}
	return &cardStore{base: base, token: token, owner: owner, client: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (s *cardStore) request(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Content-Type", "application/json")
	res, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("card store transport unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("card store HTTP %d", res.StatusCode)
	}
	if out != nil {
		return json.NewDecoder(io.LimitReader(res.Body, 32<<20)).Decode(out)
	}
	return nil
}
func (s *cardStore) path(id string) string {
	p := "/api/card-store/" + url.PathEscape(s.owner)
	if id != "" {
		p += "/" + url.PathEscape(id)
	}
	return p
}
func (d *decisionService) loadRemote() error {
	store, err := newCardStore(d.engine.name)
	if err != nil {
		return err
	}
	d.store = store
	if store == nil {
		return nil
	}
	remote := map[string]bool{}
	for offset := 0; ; offset += 10 {
		var data struct {
			Items []*Decision `json:"items"`
		}
		if err = store.request(context.Background(), http.MethodGet, fmt.Sprintf("%s?offset=%d&limit=10", store.path(""), offset), nil, &data); err != nil {
			return err
		}
		for _, v := range data.Items {
			if v.OwnerAutomon != d.engine.name || v.ID == "" {
				return fmt.Errorf("invalid remote card owner")
			}
			switch v.Status {
			case "sending":
				v.Status = "send_unknown"
			case "updating":
				v.Status = "update_unknown"
			case "dispatching":
				v.Status = "delivery_unknown"
			}
			d.items[v.ID] = v
			remote[v.ID] = true
		}
		if len(data.Items) < 10 {
			break
		}
		if offset >= 10000 {
			return fmt.Errorf("card store capacity exceeded")
		}
	}
	// The session store is already project-scoped, so legacy ownership is known.
	for id, v := range d.items {
		if remote[id] {
			continue
		}
		v.OwnerAutomon = d.engine.name
		if err = d.persistRemote(v); err != nil {
			return fmt.Errorf("import legacy card: %w", err)
		}
	}
	return nil
}
func (d *decisionService) persistRemote(v *Decision) error {
	before := v.StoreRevision
	v.StoreRevision++
	payload := map[string]any{"expected_store_revision": before, "snapshot": v}
	err := d.store.request(context.Background(), http.MethodPut, d.store.path(v.ID), payload, nil)
	if err != nil {
		// The endpoint compares the exact snapshot on retry; never invent a new
		// interaction timestamp or version after an uncertain response.
		err = d.store.request(context.Background(), http.MethodPut, d.store.path(v.ID), payload, nil)
	}
	if err != nil {
		v.StoreRevision = before
	}
	return err
}

// Management commands are durable and scoped to the authenticated Automon.
// They never dispatch a conversation. Replays use the original card revision.
func (d *decisionService) pollCardCommands() {
	if d.store == nil || d.loadErr != nil {
		return
	}
	d.mu.Lock()
	if time.Since(d.lastCommandPoll) < 5*time.Second {
		d.mu.Unlock()
		return
	}
	d.lastCommandPoll = time.Now()
	d.mu.Unlock()
	path := "/api/card-commands/" + url.PathEscape(d.engine.name)
	var queue struct {
		Items []struct {
			ID      string                 `json:"command_id"`
			Request interactiveCardRequest `json:"request"`
		} `json:"items"`
	}
	if err := d.store.request(d.engine.ctx, http.MethodGet, path, nil, &queue); err != nil {
		return
	}
	for _, cmd := range queue.Items {
		ctx, cancel := context.WithTimeout(d.engine.ctx, 25*time.Second)
		v, err := d.manageTrusted(ctx, cmd.Request, DecisionOrigin{}, true)
		cancel()
		result := map[string]any{"card": v}
		if err != nil {
			result["error"] = err.Error()
		}
		if err = d.store.request(d.engine.ctx, http.MethodPut, path+"/"+url.PathEscape(cmd.ID), result, nil); err != nil {
			return
		}
		break
	}
}

func (d *decisionService) runCardCommands() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-d.engine.ctx.Done():
			return
		case <-ticker.C:
			d.pollCardCommands()
		}
	}
}
