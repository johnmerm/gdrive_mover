package gdrive_mover

import (
	"context"
	"encoding/json"
	"fmt"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
	"io/ioutil"
	"os"
	"strings"
)

// const filename = "general-project-379412-f9fa8a0c3473.json"
const FILENAME = "client_secret_579110842346-or8rc5d44rscjqqfs2oqrtu6tlvog7pj.apps.googleusercontent.com.json"

type GDrive struct {
	name string
	srv  *drive.Service
}

func GetConfig(name string, readonly bool) (*GDrive, error) {
	var filename string
	if envFilename := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"); envFilename != "" {
		filename = envFilename
	} else {
		// Find files in current folder ending with .json
		files, err := os.ReadDir(".")
		if err != nil {
			return nil, fmt.Errorf("unable to read current directory: %v", err)
		}
		for _, file := range files {
			if !file.IsDir() && strings.HasSuffix(file.Name(), ".apps.googleusercontent.com.json") {
				filename = file.Name()
				break
			}
		}
	}

	// Load the Google Cloud Platform credentials file
	credentialsFile, err := os.Open(filename)
	if err != nil {
		return nil, fmt.Errorf("unable to read credentials file: %v", err)
	}
	defer credentialsFile.Close()

	credentialsData, err := ioutil.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("unable to read credentials file: %v", err)
	}

	// Parse the credentials file to get the oauth2.Config object
	config, err := google.ConfigFromJSON(credentialsData, drive.DriveReadonlyScope)
	if err != nil {
		return nil, fmt.Errorf("unable to parse credentials file: %v", err)
	}
	config.RedirectURL = fmt.Sprintf("http://localhost:8088/%s/callback", name)
	if readonly {
		config.Scopes = []string{drive.DriveReadonlyScope}
	} else {
		config.Scopes = []string{drive.DriveScope}
	}

	// Try to read the token from a local file
	token, err := tokenFromFile(fmt.Sprintf("token_%s.json", name))
	if err != nil {
		// If the token file is not found or invalid, obtain a new token from the web
		token, err = getTokenFromWeb(name, config)
		if err != nil {
			return nil, fmt.Errorf("unable to get token from web: %v", err)
		}

		// Save the token to a local file for future use
		err = saveToken(fmt.Sprintf("token_%s.json", name), token)
		if err != nil {
			return nil, fmt.Errorf("unable to save token: %v", err)
		}
	}

	// Create a new Drive client using the token and config
	client := config.Client(context.Background(), token)
	srv, err := drive.NewService(context.Background(), option.WithHTTPClient(client))
	if err != nil {
		return nil, fmt.Errorf("unable to create Drive client: %v", err)
	}

	return &GDrive{name, srv}, nil
}

// tokenFromFile retrieves a token from a local file.
func tokenFromFile(file string) (*oauth2.Token, error) {
	tokenFile, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer tokenFile.Close()
	t := &oauth2.Token{}
	err = json.NewDecoder(tokenFile).Decode(t)
	return t, err
}
func saveToken(file string, token *oauth2.Token) error {
	// Open the file for writing
	tokenFile, err := os.Create(file)
	if err != nil {
		return fmt.Errorf("unable to create token file: %v", err)
	}
	defer tokenFile.Close()

	// Encode the token to JSON and write it to the file
	err = json.NewEncoder(tokenFile).Encode(token)
	if err != nil {
		return fmt.Errorf("unable to encode token: %v", err)
	}
	return nil
}
