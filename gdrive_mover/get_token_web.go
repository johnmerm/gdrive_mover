package gdrive_mover

import (
	"context"
	"errors"
	"fmt"
	"github.com/gosuri/uiprogress"
	"github.com/gosuri/uiprogress/util/strutil"
	"golang.org/x/oauth2"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
	"html/template"
	"log"
	"math"
	"strconv"

	"net/http"
	"os/exec"
)

type Result[T any] struct {
	Code T
	Err  error
	Done bool
}
type CodeResult = Result[string]

func FormatSize(size int64) string {
	units := []string{"B", "KB", "MB", "GB", "TB", "PB", "EB"}
	base := int64(1024)
	if size < base {
		return strconv.FormatInt(size, 10) + " B"
	}
	exp := int(math.Log(float64(size)) / math.Log(float64(base)))
	pre := units[exp]
	num := int64(math.Pow(float64(base), float64(exp)))
	val := float64(size) / float64(num)
	return fmt.Sprintf("%.1f %s", val, pre)
}

type FileWebHodler struct {
	Files      []*drive.File
	FormatSize func(size int64) string
	Type       string
}

func StartServer(gd *GDrive, targetGD *GDrive) {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_, err := fmt.Fprintf(w, "Server running")
		if err != nil {
			log.Printf("unable to write to response: %v", err)
		}
	})

	http.HandleFunc("/directories", func(w http.ResponseWriter, r *http.Request) {
		tmpl, tmplErr := template.New("files.html").Funcs(template.FuncMap{
			"formatSize": FormatSize,
		}).ParseFiles("files.html")
		if tmplErr != nil {
			log.Fatalf("Unable to parse template: %v", tmplErr)
		}
		files, err := gd.ListFolders()
		if err != nil {

			var error *googleapi.Error
			switch {
			case errors.As(err, &error):
				log.Printf("Error Body: %s", err.(*googleapi.Error).Body)
			}
			log.Fatalf("Unable to retrieve files: %v", err)

		}
		data := FileWebHodler{
			Type:       "directories",
			Files:      files,
			FormatSize: FormatSize,
		}

		err = tmpl.Execute(w, data)
	})

	http.HandleFunc("/files", func(w http.ResponseWriter, r *http.Request) {
		tmpl, tmplErr := template.New("files.html").Funcs(template.FuncMap{
			"formatSize": FormatSize,
		}).ParseFiles("files.html")
		if tmplErr != nil {
			log.Fatalf("Unable to parse template: %v", tmplErr)
		}
		files, err := gd.ListFiles()
		if err != nil {

			var error *googleapi.Error
			switch {
			case errors.As(err, &error):
				log.Printf("Error Body: %s", err.(*googleapi.Error).Body)
			}
			log.Fatalf("Unable to retrieve files: %v", err)

		}
		data := FileWebHodler{
			Type:       "files",
			Files:      files,
			FormatSize: FormatSize,
		}

		err = tmpl.Execute(w, data)
	})
	http.HandleFunc("/{type}/transfer", func(w http.ResponseWriter, r *http.Request) {
		// Assure method is POST
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			_, _ = fmt.Fprintf(w, "Method not allowed")
			return
		}
		moveType := r.PathValue("type")

		//Get Parameters from body
		err := r.ParseForm()
		if err != nil {
			log.Fatalf("Unable to parse form: %v", err)
		}
		fileIds := r.Form["fileId"]
		if fileIds == nil {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprintf(w, "fileId missing")
			return
		}

		for _, fileId := range fileIds {
			hErr := handleOne(moveType, fileId, gd, targetGD)
			if hErr != nil {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = fmt.Fprintf(w, "Error: %v", hErr)
				return
			}
		}

		//uiprogress.Stop()
		http.Redirect(w, r, "/files", http.StatusFound)
	})

	go func() {
		err := http.ListenAndServe(":8088", nil)
		if err != nil {
			log.Printf("unable to start server: %v", err)
		}
	}()
}

func handleOne(moveType string, fileId string, gd *GDrive, targetGD *GDrive) error {
	file, err := gd.GetFile(fileId)
	if err != nil {
		log.Fatalf("Unable to retrieve file: %v", err)
	}

	var progress chan ProgressResult
	if moveType == "files" {
		progress = gd.Transfer(file, targetGD, true)
	} else if moveType == "directories" {
		progress = gd.TransferFolder(file, targetGD, true)
	} else {
		return fmt.Errorf("Unknown type %s", moveType)
	}

	bar := uiprogress.AddBar(10000).AppendCompleted().PrependFunc(func(b *uiprogress.Bar) string {
		return fmt.Sprintf("%s %s",
			strutil.PadLeft(file.Name, 50, ' '),
			strutil.PadLeft(b.TimeElapsedString(), 5, ' '),
		)
	})

	var oldVal = ForCode(-3)
	for {
		select {
		case p := <-progress:
			if p.Err != nil {
				log.Printf("Error: %v", p.Err)
				return p.Err
			} else {
				if p.Code != oldVal {
					bar.Set(int(p.Code.Progress * 100))
					oldVal = p.Code
				}
				if p.Done {
					log.Printf("Done File %s!", file.Name)
					return nil
				}
			}
		}
	}
}

func callBackHandler(name string, ch chan CodeResult) {
	http.HandleFunc(fmt.Sprintf("/%s/callback", name), func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		if code == "" {
			_, err := fmt.Fprintf(w, "Authorization code missing for %s", name)
			if err != nil {
				log.Printf("unable to write to response: %v", err)
				ch <- CodeResult{Err: err}

			}
			ch <- CodeResult{Err: fmt.Errorf("authorization code missing")}
		} else {
			_, err := fmt.Fprintf(w, "Authorization code for %s is %s", name, code)
			if err != nil {
				log.Printf("unable to write to response: %v", err)
				ch <- CodeResult{Err: err}
			}
			ch <- CodeResult{Code: code}
		}
	})

}

func openBrowser(name string, authURL string, ch chan CodeResult) error {
	go callBackHandler(name, ch)
	// Open a browser window and prompt the user to authorize the app
	err := exec.Command("open", authURL).Run()
	if err != nil {
		return fmt.Errorf("unable to open browser for %s: %v", name, err)
	}
	return nil
}

func printAndScanInput(name string, authURL string) (string, error) {
	fmt.Printf("Go to the following link in your browser: \n\n%s\n\n", authURL)
	fmt.Println("Enter the authorization code:")
	ch := make(chan CodeResult)
	gotIt := make(chan string)
	defer close(ch)
	defer close(gotIt)
	err := openBrowser(name, authURL, ch)
	if err != nil {
		log.Printf("unable to open browser for %s: %v", name, err)
		return "", nil
	}

	// Read the authorization code from the command line

	go func() {
		var code string
		for {
			select {
			case result := <-gotIt:
				code = result
				log.Printf("Got code: %s from %s callback", code, name)
				break
			default:
				log.Println("waiting for code")
				_, err = fmt.Scan(&code)
				if err != nil {
					ch <- CodeResult{Err: err}
				} else {
					ch <- CodeResult{Code: code}
				}
			}
		}

	}()
	result := <-ch
	if result.Err != nil {
		return "", result.Err
	} else {
		return result.Code, nil
	}

}

func getTokenFromWeb(name string, config *oauth2.Config) (*oauth2.Token, error) {
	// Generate a URL for the user to visit to authorize the app
	authURL := config.AuthCodeURL("state-token", oauth2.AccessTypeOffline)

	code, err := printAndScanInput(name, authURL)

	// Exchange the authorization code for a token
	token, err := config.Exchange(context.Background(), code)

	if err != nil {
		return nil, fmt.Errorf("unable to exchange authorization code for token: %v", err)
	}
	return token, nil
}
