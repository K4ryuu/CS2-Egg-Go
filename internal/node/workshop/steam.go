// SPDX-License-Identifier: GPL-3.0-or-later

package workshop

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// DetailsURL is the public endpoint that tells which version of a workshop
// item is current. No API key, no login: it only reads published metadata.
var DetailsURL = "https://api.steampowered.com/ISteamRemoteStorage/GetPublishedFileDetails/v1/"

// Detail is what Steam says about one item.
type Detail struct {
	ID       string
	Manifest string // hcontent_file, the version id
	Updated  int64  // time_updated
	Size     int64
	Missing  bool // the item is gone or private
}

// Details asks Steam about a batch of items.
func Details(ctx context.Context, client *http.Client, ids []string) (map[string]Detail, error) {
	if len(ids) == 0 {
		return map[string]Detail{}, nil
	}
	form := url.Values{}
	form.Set("itemcount", strconv.Itoa(len(ids)))
	for i, id := range ids {
		form.Set("publishedfileids["+strconv.Itoa(i)+"]", id)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, DetailsURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "cs2node")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("steam api returned HTTP %d", resp.StatusCode)
	}
	var body struct {
		Response struct {
			Details []struct {
				ID       string `json:"publishedfileid"`
				Result   int    `json:"result"`
				Manifest string `json:"hcontent_file"`
				Updated  int64  `json:"time_updated"`
				Size     any    `json:"file_size"` // a number on some items, a string on others
			} `json:"publishedfiledetails"`
		} `json:"response"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&body); err != nil {
		return nil, err
	}
	out := make(map[string]Detail, len(body.Response.Details))
	for _, d := range body.Response.Details {
		item := Detail{ID: d.ID, Manifest: d.Manifest, Updated: d.Updated, Missing: d.Result != 1}
		switch v := d.Size.(type) {
		case float64:
			item.Size = int64(v)
		case string:
			item.Size = atoi(v)
		}
		out[d.ID] = item
	}
	return out, nil
}
