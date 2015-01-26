package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"path"
)

// S3Redirector implements the ContentStorer interface to serve content via redirecting to S3.
type S3Redirector struct {
}

// Get will use the provided object Meta data to write a redirect Location and status to
// the Response Writer. It generates a signed S3 URL that is valid for 5 minutes.
func (s *S3Redirector) Get(meta *Meta, w http.ResponseWriter, r *http.Request) int {
	token := S3SignQuery("GET", path.Join("/", meta.PathPrefix, oidPath(meta.Oid)), 300)
	w.Header().Set("Location", token.Location)
	w.WriteHeader(302)
	return 302
}

// PutLink generates an signed S3 link that will allow the client to PUT data into S3. This
// link includes the x-amz-content-sha256 header which will ensure that the client uploads only
// data that will match the OID.
func (s *S3Redirector) PutLink(meta *Meta) *link {
	token := S3SignHeader("PUT", path.Join("/", meta.PathPrefix, oidPath(meta.Oid)), meta.Oid)
	header := make(map[string]string)
	header["Authorization"] = token.Token
	header["x-amz-content-sha256"] = meta.Oid
	header["x-amz-date"] = token.Time.Format(isoLayout)

	return &link{Href: token.Location, Header: header}
}

// Exists checks to see if an object exists on S3.
func (s *S3Redirector) Exists(meta *Meta) (bool, error) {
	token := S3SignQuery("HEAD", path.Join("/", meta.PathPrefix, oidPath(meta.Oid)), 30)
	req, err := http.NewRequest("HEAD", token.Location, nil)
	if err != nil {
		return false, err
	}

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, err
	}

	if res.StatusCode != 200 {
		return false, err
	}

	return true, nil
}

func oidPath(oid string) string {
	dir := path.Join(oid[0:2], oid[2:4])

	return path.Join("/", dir, oid)
}

// MetaStore implements the MetaStorer interface to provide metadata from an external HTTP API.
type MetaStore struct {
}

// MetaLink generates a URI path using the configured MetaEndpoint.
func (s *MetaStore) MetaLink(v *RequestVars) string {
	return Config.MetaEndpoint + "/" + path.Join(v.User, v.Repo, "media", "blobs", v.Oid)
}

// Get retrieves metadata from the backend API.
func (s *MetaStore) Get(v *RequestVars) (*Meta, error) {
	req, err := http.NewRequest("GET", s.MetaLink(v), nil)
	if err != nil {
		logger.Printf("[META] error - %s", err)
		return nil, err
	}
	req.Header.Set("Accept", Config.ApiMediaType)
	if v.Authorization != "" {
		req.Header.Set("Authorization", v.Authorization)
	}

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		logger.Printf("[META] error - %s", err)
		return nil, err
	}

	defer res.Body.Close()
	if res.StatusCode == 200 {
		var m Meta
		dec := json.NewDecoder(res.Body)
		err := dec.Decode(&m)
		if err != nil {
			logger.Printf("[META] error - %s", err)
			return nil, err
		}

		logger.Printf("[META] status - %d", res.StatusCode)
		return &m, nil
	}

	if res.StatusCode == 204 {
		return &Meta{Oid: v.Oid, Size: v.Size, PathPrefix: v.PathPrefix}, nil
	}

	logger.Printf("[META] status - %d", res.StatusCode)
	return nil, fmt.Errorf("status: %d", res.StatusCode)
}

// Send POSTs metadata to the backend API.
func (s *MetaStore) Send(v *RequestVars) (*Meta, error) {
	req, err := signedApiPost(s.MetaLink(v), v)

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		logger.Printf("[META] error - %s", err)
		return nil, err
	}

	defer res.Body.Close()

	if res.StatusCode == 403 {
		logger.Printf("[META] 403")
		return nil, apiAuthError
	}

	if res.StatusCode == 200 || res.StatusCode == 201 {
		var m Meta
		dec := json.NewDecoder(res.Body)
		err := dec.Decode(&m)
		if err != nil {
			logger.Printf("[META] error - %s", err)
			return nil, err
		}
		m.existing = res.StatusCode == 200
		logger.Printf("[META] status - %d", res.StatusCode)
		return &m, nil
	}

	logger.Printf("[META] status - %d", res.StatusCode)
	return nil, fmt.Errorf("status: %d", res.StatusCode)
}

// Verify is used during the callback phase to indicate to the backend API that the
// object has been received.
func (s *MetaStore) Verify(v *RequestVars) error {
	url := Config.MetaEndpoint + "/" + path.Join(v.User, v.Repo, "media", "blobs", "verify", v.Oid)

	req, err := signedApiPost(url, v)
	if err != nil {
		return err
	}

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		logger.Printf("[VERIFY] error - %s", err)
		return err
	}

	defer res.Body.Close()

	if res.StatusCode == 403 {
		logger.Printf("[VERIFY] 403")
		return apiAuthError
	}

	logger.Printf("[VERIFY] status - %d", res.StatusCode)
	if res.StatusCode == 200 {
		return nil
	}
	return fmt.Errorf("status: %d", res.StatusCode)
}

func signedApiPost(url string, v *RequestVars) (*http.Request, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.Encode(&Meta{Oid: v.Oid, Size: v.Size})

	req, err := http.NewRequest("POST", url, &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", Config.ApiMediaType)
	if v.Authorization != "" {
		req.Header.Set("Authorization", v.Authorization)
	}

	if Config.HmacKey != "" {
		mac := hmac.New(sha256.New, []byte(Config.HmacKey))
		mac.Write(buf.Bytes())
		req.Header.Set("Content-Hmac", "sha256 "+hex.EncodeToString(mac.Sum(nil)))
	}

	return req, nil
}
