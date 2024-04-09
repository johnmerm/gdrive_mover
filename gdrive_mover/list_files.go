package gdrive_mover

import (
	"fmt"
	"google.golang.org/api/googleapi"
	"log"
	"sort"

	"google.golang.org/api/drive/v3"
)

const fileFields = "files(id, name, mimeType,size,quotaBytesUsed,parents,owners,md5Checksum,originalFilename,createdTime,modifiedTime)"
const fileFieldList = "nextPageToken, " + fileFields

func (gd GDrive) GetFile(id string) (*drive.File, error) {
	return gd.srv.Files.Get(id).Fields("*").Do()
}

func (gd GDrive) GetFileAndFolders(id string) ([]*drive.File, error) {
	var files = make([]*drive.File, 0)
	self, err := gd.GetFile(id)
	if err != nil {
		return nil, err
	} else {
		files = append(files, self)
		for _, pid := range self.Parents {
			parents, err := gd.GetFileAndFolders(pid)
			if err != nil {
				return nil, err
			}
			files = append(files, parents...)
		}
	}
	return files, nil
}

func (gd GDrive) ListFolders() ([]*drive.File, error) {
	srv := gd.srv
	var files []*drive.File
	nextPageToken := ""
	for {
		q := srv.Files.List().
			Fields(fileFieldList).
			Q("mimeType='application/vnd.google-apps.folder' and 'me' in owners").
			OrderBy("quotaBytesUsed desc")
		if nextPageToken != "" {
			q.PageToken(nextPageToken)
		}
		r, err := q.Do()
		if err != nil {
			return nil, err
		}
		files = append(files, r.Files...)
		nextPageToken = r.NextPageToken
		if nextPageToken == "" {
			break
		}
	}
	return files, nil
}

// ListFiles returns a list of all files in the account
func (gd GDrive) ListFiles() ([]*drive.File, error) {
	srv := gd.srv
	var files []*drive.File
	nextPageToken := ""
	for {
		q := srv.Files.List().
			Fields(fileFieldList).
			Q("'me' in owners").
			OrderBy("quotaBytesUsed desc")
		if nextPageToken != "" {
			q.PageToken(nextPageToken)
		}
		r, err := q.Do()
		if err != nil {
			return nil, err
		}
		files = append(files, r.Files...)
		nextPageToken = r.NextPageToken
		if nextPageToken == "" {
			break
		}
	}
	return files, nil
}

// Helper function to sort the files by size in descending order
func sortFilesBySize(files []*drive.File) {
	sort.Slice(files, func(i, j int) bool {
		return files[i].Size > files[j].Size
	})
}

type ProgressConstruct struct {
	Progress     float32
	fileId       string
	fileName     string
	destFileId   string
	destFileName string
}

type ProgressResult = Result[ProgressConstruct]

func (gd GDrive) createParentFolder(pid string, targetGD *GDrive) (*drive.File, error) {
	parent, err := gd.GetFile(pid)
	pName := parent.Name
	if pName == "My Drive" {
		return nil, nil
	}
	if err != nil {
		switch err.(type) {
		case *googleapi.Error:
			log.Printf("Error Body: %s", err.(*googleapi.Error).Body)
		}
		log.Fatalf("Unable to retrieve files: %v", err)
	}
	var parents = make([]string, 0)
	if parent.Parents != nil {
		for _, pid := range parent.Parents {
			p, err := gd.createParentFolder(pid, targetGD)
			if err != nil {
				return nil, err
			} else if p != nil {
				parents = append(parents, p.Id)
			}
		}
	}
	if len(parents) == 0 {
		parents = nil
	}
	//Try to find the Folder by name in the give parent
	q := fmt.Sprintf("name='%s' and mimeType='application/vnd.google-apps.folder'", parent.Name)
	if parents != nil {
		q = fmt.Sprintf("%s and '%s' in parents", q, parents[0])
	}
	r, err := targetGD.srv.Files.List().Q(q).Fields("files(id, name)").Do()
	if err == nil && r.Files != nil && len(r.Files) > 0 {
		return r.Files[0], nil
	}

	// Create the folder on the target account
	req := targetGD.srv.Files.Create(&drive.File{
		Name:     parent.Name,
		MimeType: "application/vnd.google-apps.folder",
		Parents:  parents,
	})
	return req.Do()
}

func (gd GDrive) GetFilesInFolder(folder *drive.File) ([]*drive.File, error) {
	var files = make([]*drive.File, 0)
	q := fmt.Sprintf("'%s' in parents", folder.Id)
	r, err := gd.srv.Files.List().Q(q).Fields(fileFields).Do()
	if err != nil {
		return nil, err
	}
	files = append(files, r.Files...)

	return files, nil
}

func (gd GDrive) GetFolderSize(folder *drive.File) (int64, int64, error) {
	var size int64 = 0
	var quotaBytesUsed int64 = 0
	q := fmt.Sprintf("'%s' in parents", folder.Id)
	r, err := gd.srv.Files.List().Q(q).Fields(fileFields).Do()
	if err != nil {
		return 0, 0, err
	}
	for _, f := range r.Files {
		if f.MimeType == "application/vnd.google-apps.folder" {
			s, q, err := gd.GetFolderSize(f)
			if err != nil {
				return 0, 0, err
			}
			size += s
			quotaBytesUsed += q
		}
		size += f.Size
		quotaBytesUsed += f.QuotaBytesUsed
	}
	return size, quotaBytesUsed, nil
}

func (gd GDrive) TransferFolder(file *drive.File, targetGD *GDrive, shareBack bool) chan ProgressResult {
	progress := make(chan ProgressResult)
	go func() {
		//List folder Contents
		files, err := gd.GetFilesInFolder(file)
		if err != nil {
			progress <- ProgressResult{Code: ForCode(-2), Err: err, Done: true}
			return
		}
		for _, file := range files {
			mimeType := file.MimeType
			if mimeType == "application/vnd.google-apps.folder" {
				progress <- <-gd.TransferFolder(file, targetGD, false)
			} else {
				//Transfer file
				progress <- <-gd.Transfer(file, targetGD, false)
			}
		}
		progress <- ProgressResult{ForCode(100), nil, true}
		defer close(progress)
		if shareBack {
			share(file, nil, targetGD)
		}
	}()

	return progress
}

func (gd GDrive) Transfer(file *drive.File, targetGD *GDrive, shareBack bool) chan ProgressResult {
	progress := make(chan ProgressResult)
	go func() {
		// Retrieve the file content
		newFile, err := gd.srv.Files.Get(file.Id).Fields("*").Do()
		if err != nil {
			fmt.Printf("Unable to get file metadata: %v\n", err)
			progress <- ProgressResult{ForCode(-2), err, true}
			return
		}

		//Create the folder structure
		var parents = make([]string, 0)
		for _, pid := range newFile.Parents {
			p, err := gd.createParentFolder(pid, targetGD)
			if err != nil {
				progress <- ProgressResult{ForCode(-2), err, true}
				return
			} else if p != nil {
				parents = append(parents, p.Id)
			}
		}

		resp, err := gd.srv.Files.Get(file.Id).Download()
		if err != nil {
			fmt.Printf("Unable to download file: %v\n", err)
			progress <- ProgressResult{ForCode(-2), err, true}
			return
		}
		defer resp.Body.Close()

		// Create a new file on the target account
		req := targetGD.srv.Files.Create(&drive.File{

			Name:             newFile.Name,
			OriginalFilename: newFile.OriginalFilename,
			Parents:          parents,
		})
		req.Header().Set("Content-Length", fmt.Sprintf("%d", newFile.Size))
		req.Media(resp.Body)

		req.ProgressUpdater(func(current, total int64) {
			if total == 0 {
				total = newFile.Size
			}
			if total > 0 && current > 0 {
				progress <- ProgressResult{ForProgress(float32(current*100)/float32(total), newFile, nil), nil, false}
			}
		})
		dlFile, err := req.Do()
		if err != nil {
			log.Printf("Unable to upload file: %v\n", err)
			switch err.(type) {
			case *googleapi.Error:
				log.Printf("Error Body: %s", err.(*googleapi.Error).Body)
			default:
				log.Printf("Error: %v", err)
			}
			progress <- ProgressResult{ForCode(0), err, true}
		} else {
			log.Printf("File %s uploaded successfully\n", dlFile.Name)
			dlCheckSum := dlFile.Md5Checksum

			if dlCheckSum == "" {
				//Fetch uploaded file's checkSum
				uploadedFile, err := targetGD.srv.Files.Get(dlFile.Id).Fields("md5Checksum").Do()
				if err != nil {
					log.Printf("Unable to get uploaded file's checksum: %v\n", err)
					progress <- ProgressResult{ForProgress(100, newFile, dlFile), err, true}
				} else {
					dlCheckSum = uploadedFile.Md5Checksum
				}
			}

			if dlCheckSum != newFile.Md5Checksum {
				log.Printf("File %s uploaded successfully but the checksums do not match\n", dlFile.Name)
				progress <- ProgressResult{ForProgress(100, newFile, dlFile), fmt.Errorf("File %s uploaded successfully but the checksums do not match\n", dlFile.Name), true}
			} else {
				progress <- ProgressResult{ForProgress(100, newFile, dlFile), nil, true}
				//Delete src file
				deleteErr := gd.srv.Files.Delete(file.Id).Do()
				if deleteErr != nil {
					log.Printf("Unable to delete file: %v\n", err)
				} else if shareBack {
					share(newFile, dlFile, targetGD)
				}
			}

		}
		defer close(progress)
	}()
	return progress

}

func ForCode(status float32) ProgressConstruct {
	return ProgressConstruct{Progress: status}
}

func ForProgress(progress float32, newFile *drive.File, dlFile *drive.File) ProgressConstruct {
	if dlFile == nil {
		return ProgressConstruct{
			Progress: progress,
			fileId:   newFile.Id,
			fileName: newFile.Name,
		}
	} else {
		return ProgressConstruct{
			Progress:     progress,
			fileId:       newFile.Id,
			fileName:     newFile.Name,
			destFileId:   dlFile.Id,
			destFileName: dlFile.Name,
		}
	}

}

func share(newFile *drive.File, dlFile *drive.File, targetGD *GDrive) {
	//Share file from tgt to src
	perm := &drive.Permission{
		Type:         "user",
		Role:         "reader",
		EmailAddress: newFile.Owners[0].EmailAddress,
	}
	shareFile, shareFileErr := targetGD.srv.Permissions.Create(dlFile.Id, perm).Do()
	if shareFileErr != nil {
		log.Printf("Unable to share file: %v\n", shareFileErr)
	} else {
		log.Printf("File %s shared successfully\n", shareFile.Id)
	}
}
