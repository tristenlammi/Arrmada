package jellyfin

import (
	"context"
	"errors"
	"net/http"
	"net/url"
)

// UserImage fetches a user's picture (GET /UserImage?userId=…; Emby's
// /Users/{id}/Images/Primary) through the client, like
// Image, so a browser never needs the server's address or key. A user without a picture
// is ErrNotFound. Caller closes the body.
func (c *Client) UserImage(ctx context.Context, userID string) (*http.Response, error) {
	if c.base == "" {
		return nil, ErrNotConfigured
	}
	if !(isItemID(userID) || (c.emby && isEmbyItemID(userID))) {
		return nil, errors.New("invalid image request")
	}
	op, target := "GET /UserImage", c.base+"/UserImage?"+url.Values{"userId": {userID}}.Encode()
	if c.emby {
		// Emby keeps the older route: /Users/{id}/Images/Primary.
		op, target = "GET /Users/{id}/Images/Primary", c.apiBase()+"/Users/"+url.PathEscape(userID)+"/Images/Primary"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, &Error{Op: op, Kind: ErrNotConfigured, Detail: "the server address isn't a valid URL"}
	}
	c.setAuth(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, &Error{Op: op, Kind: ErrUnreachable, Detail: netDetail(err)}
	}
	if resp.StatusCode >= 300 {
		resp.Body.Close()
		return nil, statusError(op, resp.StatusCode, nil)
	}
	return resp, nil
}
