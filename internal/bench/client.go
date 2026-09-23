package bench

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/google/uuid"
)

type client struct {
	http *http.Client
	base string
	key  string
}

type apiError struct {
	Status int
	Code   string
	Detail string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("%d %s: %s", e.Status, e.Code, e.Detail)
}

func (c *client) do(ctx context.Context, method, path string, body any, idempotent bool, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if idempotent {
		req.Header.Set("Idempotency-Key", "bench-"+uuid.NewString())
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		var problem struct {
			Code   string `json:"code"`
			Detail string `json:"detail"`
		}
		_ = json.Unmarshal(raw, &problem)
		if problem.Code == "" {
			problem.Code = strings.ToLower(strings.ReplaceAll(http.StatusText(resp.StatusCode), " ", "_"))
		}
		return &apiError{Status: resp.StatusCode, Code: problem.Code, Detail: problem.Detail}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

type resource struct {
	ID string `json:"id"`
}
