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
}

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

func GetIndex(typeIndex string) (bleve.Index, error) {
	indexMapping := bleve.NewIndexMapping()
	AddTypeMapping(indexMapping, mapIndexType[typeIndex])
	blevePath := typeIndex
	i, err1 := bleve.Open(blevePath)
	if err1 == bleve.ErrorIndexPathDoesNotExist {
		fmt.Printf("Creating new index %s...", typeIndex)
		i, err2 := bleve.New(blevePath, indexMapping)
		if err2 != nil {
			fmt.Printf("Error on creating new Index: %s\n", err2)
			return i, err2
		}
		FillIndex(i, mapIndexType[typeIndex])
		return i, nil

	} else if err1 != nil {
		fmt.Printf("Error on creating new Index %s: %s\n", typeIndex, err1)
		return i, err1
	}
	fmt.Printf("found existing Index")
	return i, nil

}

func FillIndex(index bleve.Index, docType string) {

	var docs []file
	GetFileDocs(index, docType, &docs)
	// See for using batch instead : batch := index.NewBatch()
	for i := range docs {
		docs[i].DocType = docType
		index.Index(docs[i].ID, docs[i])
	}

}

func GetFileDocs(index bleve.Index, docType string, docs *[]file) {
	req := &couchdb.AllDocsRequest{Limit: 100}
	err := couchdb.GetAllDocs(inst, docType, req, docs)
	if err != nil {
		fmt.Printf("Error on unmarshall: %s\n", err)
	}
}

func QueryIndex(queryString string) ([]couchdb.JSONDoc, error) {
	var fetched []couchdb.JSONDoc

	query := bleve.NewQueryStringQuery(queryString)
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
