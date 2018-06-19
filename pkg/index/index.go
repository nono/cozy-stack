package index

import (
	"fmt"
	"time"

	"github.com/blevesearch/bleve"
	// "github.com/blevesearch/bleve/mapping"
	"github.com/cozy/cozy-stack/pkg/couchdb"
	"github.com/cozy/cozy-stack/pkg/instance"
	"github.com/cozy/cozy-stack/pkg/realtime"
)

type file struct {
	ID         string        `json:"_id"`
	Rev        string        `json:"_rev"`
	Type       string        `json:"type"`
	Name       string        `json:"name"`
	DirID      string        `json:"dir_id"`
	CreatedAt  time.Time     `json:"created_at"`
	UpdatedAt  time.Time     `json:"updated_at"`
	Size       string        `json:"size"`
	Md5Sum     string        `json:"md5sum"`
	Mime       string        `json:"mime"`
	Class      string        `json:"class"`
	Executable bool          `json:"executable"`
	Trashed    bool          `json:"trashed"` //TODO: pay attention to trash or not
	Tags       []interface{} `json:"tags"`
	DocType    string        `json:"docType"`
	Metadata   struct {
		Datetime         time.Time `json:"datetime"`
		ExtractorVersion int       `json:"extractor_version"`
		Height           int       `json:"height"`
		Width            int       `json:"width"`
	} `json:"metadata"`
	// *document
}

type photoAlbum struct {
	ID        string    `json:"_id"`
	Rev       string    `json:"_rev"`
	CreatedAt time.Time `json:"created_at"`
	Name      string    `json:"name"`
	DocType   string    `json:"docType"`
	// *document
}

// type document struct {
// 	ID      string `json:"_id"`
// 	DocType string `json:"docType"`
// }

// var typeMap map[string]interface{}

var mapIndexType map[string]string
var indexAlias bleve.Index
var inst *instance.Instance

func StartIndex(instance *instance.Instance) error {
	inst = instance

	mapIndexType = map[string]string{
		"bleve/photo.albums.bleve":  "io.cozy.photos.albums",
		"bleve/file.bleve":          "io.cozy.files",
		"bleve/bank.accounts.bleve": "io.cozy.bank.accounts",
	}

	// typeMap = map[string]interface{}{
	// 	"io.cozy.photos": photoAlbum{},
	// }

	var err error

	photoAlbumIndex, err := GetIndex("bleve/photo.albums.bleve")
	if err != nil {
		return err
	}

	fileIndex, err := GetIndex("bleve/file.bleve")
	if err != nil {
		return err
	}

	bankAccountIndex, err := GetIndex("bleve/bank.accounts.bleve")
	if err != nil {
		return err
	}
	// Creating an aliasIndex to make it clear to the user:
	indexAlias = bleve.NewIndexAlias(photoAlbumIndex, fileIndex, bankAccountIndex)

	// subscribing to changes
	eventChan := realtime.GetHub().Subscriber(inst)
	for _, value := range mapIndexType {
		eventChan.Subscribe(value)
	}

	go func() {
		for ev := range eventChan.Channel {
			var originalIndex *bleve.Index
			if ev.Doc.DocType() == "io.cozy.photos.albums" {
				originalIndex = &photoAlbumIndex
			}
			if ev.Doc.DocType() == "io.cozy.files" {
				originalIndex = &fileIndex
			}
			if ev.Doc.DocType() == "io.cozy.bank.accounts" {
				originalIndex = &bankAccountIndex
			}
			if ev.Verb == "CREATED" || ev.Verb == "UPDATED" {
				(*originalIndex).Index(ev.Doc.ID(), ev.Doc)
				fmt.Println(ev.Doc)
				fmt.Println("reindexed")
			} else if ev.Verb == "DELETED" {
				indexAlias.Delete(ev.Doc.ID())
				fmt.Println("deleted")
			} else {
				fmt.Println(ev.Verb)
			}
		}
	}()

	return nil
}

func GetIndex(indexPath string) (bleve.Index, error) {
	indexMapping := bleve.NewIndexMapping()
	AddTypeMapping(indexMapping, mapIndexType[indexPath])

	blevePath := indexPath

	i, err1 := bleve.Open(blevePath)
	if err1 == bleve.ErrorIndexPathDoesNotExist {
		fmt.Printf("Creating new index %s...", indexPath)
		i, err2 := bleve.New(blevePath, indexMapping)
		if err2 != nil {
			fmt.Printf("Error on creating new Index: %s\n", err2)
			return i, err2
		}
		FillIndex(i, mapIndexType[indexPath])
		return i, nil

	} else if err1 != nil {
		fmt.Printf("Error on creating new Index %s: %s\n", indexPath, err1)
		return i, err1
	}
	fmt.Printf("found existing Index")
	return i, nil
}

func FillIndex(index bleve.Index, docType string) {

	var docsFile []file
	var docsPhotoAlbum []photoAlbum
	if docType == "io.cozy.photos.albums" {
		GetFileDocs(docType, &docsPhotoAlbum)
		for i := range docsPhotoAlbum {
			docsPhotoAlbum[i].DocType = docType
			index.Index(docsPhotoAlbum[i].ID, docsPhotoAlbum[i])
		}
	} else {
		GetFileDocs(docType, &docsFile)
		for i := range docsFile {
			docsFile[i].DocType = docType
			index.Index(docsFile[i].ID, docsFile[i])
		}
	}

	// var docs interface{}
	// var docs []interface{}
	// docs = []interface{}{typeMap[docType]}
	// GetFileDocs(docType, &docs)
	// for i := range docs {
	// 	docs[i].DocType = docType
	// 	index.Index(docs[i].ID, docs[i])
	// }

	// var docs []document
	// if docType == "io.cozy.photos.albums" {
	// 	docs = []photoAlbum{}
	// } else {
	// 	docs = []file{}
	// }
	// GetFileDocs(docType, &docs)
	// for i := range docs {
	// 	docs[i].DocType = docType
	// 	index.Index(docs[i].ID, docs[i])
	// }

	// See for using batch instead : batch := index.NewBatch()

}

func GetFileDocs(docType string, docs interface{}) {
	req := &couchdb.AllDocsRequest{}
	err := couchdb.GetAllDocs(inst, docType, req, docs)
	if err != nil {
		fmt.Printf("Error on unmarshall: %s\n", err)
	}
}

func QueryIndex(queryString string) ([]couchdb.JSONDoc, error) {
	var fetched []couchdb.JSONDoc

	query := bleve.NewQueryStringQuery(PreparingQuery(queryString))
	search := bleve.NewSearchRequest(query)
	searchResults, err := indexAlias.Search(search)
	if err != nil {
		fmt.Printf("Error on querying: %s", err)
		return fetched, err
	}
	fmt.Printf(searchResults.String())

	var currFetched couchdb.JSONDoc
	for _, result := range searchResults.Hits {
		currFetched = couchdb.JSONDoc{}
		couchdb.GetDoc(inst, mapIndexType[result.Index], result.ID, &currFetched)
		fetched = append(fetched, currFetched)
	}
	return fetched, nil
}

func PreparingQuery(queryString string) string {
	return "*" + queryString + "*"
}
