package main

// Note: This was built off of an example reverse proxy created by Ben Church
// https://github.com/bechurch/reverse-proxy-demo

import (
	"encoding/json"
	"io/ioutil"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"time"
)

var (
	auth_endpoint_url    string
	auth_client_id       string
	auth_client_secret   string
	auth_scope           string
	proxy_downstream_url string
	proxy_port           string
	access_token         string
	token_type           string
	token_refresh_time   time.Time
	api_key              string
	api_key_header       string
)

// Structure for storing results from a
type AuthReponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
	TokenType   string `json:"token_type"`
}

// Proxies the incoming request to the downstream, adding Authorization
// header and optional API Key header
func handleRequestAndRedirect(res http.ResponseWriter, req *http.Request) {
	url, err := url.Parse(proxy_downstream_url)
	if err != nil {
		log.Print(err)
		http.Error(res, "Failure calling downstream proxy", http.StatusInternalServerError)
		return
	}

	proxy := httputil.NewSingleHostReverseProxy(url)

	req.URL.Host = url.Host
	req.URL.Scheme = url.Scheme
	req.Host = url.Host

	req.Header.Set("Authorization", token_type+" "+access_token)

	if api_key != "" {
		req.Header.Set(api_key_header, api_key)
	}

	log.Printf("Proxy %s %s", req.Method, req.URL)
	proxy.ServeHTTP(res, req)
}

// Gets (or refreshes) the access token using a jittered backed-off retry
func getOuath2AuthAccessToken() {
	request_body := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {auth_client_id},
		"client_secret": {auth_client_secret},
	}
	if auth_scope != "" {
		request_body.Set("scope", auth_scope)
	}

	retry_number := -1

	for {
		retry_number++

		if retry_number > 5 {
			log.Print("Failed to acquire access token; exiting")
			break
		} else if retry_number > 0 {
			seconds_to_wait := retry_number*retry_number + 1
			log.Printf("Failed to aquired token; awaiting retry #%v in %v seconds", retry_number, seconds_to_wait)
			retry_time := time.Duration(seconds_to_wait) * time.Second
			time.Sleep(retry_time)
			log.Printf("Retry #%v", retry_number)
		}

		log.Printf("Sending authentication request via POST to %s", auth_endpoint_url)
		resp, err := http.PostForm(auth_endpoint_url, request_body)

		if err != nil {
			log.Print(err)
			continue
		}

		if resp.StatusCode != 200 && resp.StatusCode != 201 {
			log.Printf("Received non-200 status code: %s", resp.Status)
			continue
		}

		defer resp.Body.Close()

		body, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			log.Print(err)
			continue
		}

		//TODO: Error handling on unmarshalling the JSON payload
		auth_response := AuthReponse{}
		err = json.Unmarshal(body, &auth_response)
		if err != nil {
			log.Print(err)
			continue
		}

		if auth_response.AccessToken == "" || auth_response.TokenType == "" || auth_response.ExpiresIn == 0 {
			log.Print("Returned JSON document did not contain required fields")
			continue
		}

		access_token = auth_response.AccessToken
		token_type = auth_response.TokenType
		expires := auth_response.ExpiresIn - (60 * 5)
		token_refresh_time = time.Now().UTC().Add(time.Second * time.Duration(expires))

		log.Print("Access token updated")
		log.Printf("Token refresh scheduled at %s", token_refresh_time)
		break
	}
}

// Go routine to handle token refresh on a loop
func handleTokenRefresh() {
	for {
		current_time := time.Now().UTC()
		if current_time.After(token_refresh_time) {
			log.Print("Refreshing access token")
			getOuath2AuthAccessToken()
		}
		time.Sleep(30 * time.Second)
	}
}

// Retrieves a named environment variable. validates that required
// variables are supplied, and supplies defaults for missing values
func getEnvironmentVariable(key string, required bool, secret bool, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		if secret {
			log.Printf("%s=**************", key)
		} else {
			log.Printf("%s=%s", key, value)
		}
		return value
	}

	if required {
		log.Fatalf("Environment variable %s must be supplied", key)
	}

	if fallback != "" {
		log.Printf("%s=%s (Default Value)", key, fallback)
	}
	return fallback
}

// Initialize variables from environment
func initVariables() {
	auth_endpoint_url = getEnvironmentVariable("AUTH_ENDPOINT_URL", true, false, "")
	auth_client_id = getEnvironmentVariable("AUTH_CLIENT_ID", true, true, "")
	auth_client_secret = getEnvironmentVariable("AUTH_CLIENT_SECRET", true, true, "")
	auth_scope = getEnvironmentVariable("AUTH_SCOPE", false, false, "")
	proxy_downstream_url = getEnvironmentVariable("PROXY_DOWNSTREAM_URL", true, false, "")
	proxy_port = getEnvironmentVariable("PROXY_PORT", false, false, "10801")
	api_key = getEnvironmentVariable("PROXY_API_KEY", false, true, "")
	if api_key != "" {
		api_key_header = getEnvironmentVariable("PROXY_API_KEY_HEADER", false, false, "x-api-key")
	}
}

// Main program entrypoint
func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)

	log.Print("Initializing proxy")

	initVariables()

	log.Print("Getting initial access token")
	getOuath2AuthAccessToken()

	if access_token == "" {
		log.Fatal("Failed to acquire initial access token - terminating")
	}

	log.Print("Starting access token refresh background routine")
	go handleTokenRefresh()

	listen_address := ":" + proxy_port
	http.HandleFunc("/", handleRequestAndRedirect)
	log.Printf("Listening to path / on %s", listen_address)
	if err := http.ListenAndServe(listen_address, nil); err != nil {
		panic(err)
	}
}
