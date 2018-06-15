package index

import (
	"fmt"
	"time"

	"github.com/blevesearch/bleve"
	// "github.com/blevesearch/bleve/mapping"
	"github.com/cozy/cozy-stack/pkg/couchdb"
	"github.com/cozy/cozy-stack/pkg/instance"
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
	Trashed    bool          `json:"trashed"` //pay attention to trash or not
	Tags       []interface{} `json:"tags"`
	DocType    string        `json:"docType"`
	Metadata   struct {
		Datetime         time.Time `json:"datetime"`
		ExtractorVersion int       `json:"extractor_version"`
		Height           int       `json:"height"`
		Width            int       `json:"width"`
	} `json:"metadata"`
}

func StartIndex(instance *instance.Instance) (bleve.Index, error) {

	indexMapping := bleve.NewIndexMapping()
	indexMapping = addPhotoAlbumMapping(indexMapping)
	indexMapping = addFileMapping(indexMapping)
	indexMapping = addBankAccountMapping(indexMapping)
	indexMapping.TypeField = "DocType"

	// TODO : choose to make multiple index and indexalias instead of unique index
	// indexAlias := bleve.NewIndexAlias(indexMapping)
	// indexAlias.Index(id, data)

	blevePath := "index.bleve"
	i, err1 := bleve.Open(blevePath)
	if err1 == bleve.ErrorIndexPathDoesNotExist {
		fmt.Println("Creating new index...")
		i, err2 := bleve.New(blevePath, indexMapping)
		if err2 != nil {
			fmt.Println("Error on creating new Index: %s\n", err2)
			return i, err2
		}
		FillIndex(i, instance)
		return i, nil

	} else if err1 != nil {
		fmt.Println("Error on creating new Index: %s\n", err1)
		return i, err1
	}
	fmt.Println("found existing Index")
	return i, nil

	// test(i)
}

func FillIndex(index bleve.Index, instance *instance.Instance) {

	FillFilesIndex(index, instance)
	FillAlbumPhotosIndex(index, instance)

	// test(index)

}

func FillFilesIndex(index bleve.Index, instance *instance.Instance) {

	var docs []file
	GetFileDocs(index, instance, "io.cozy.files", &docs)
	// See for using batch instead : batch := index.NewBatch()
	for i := range docs {
		docs[i].DocType = "io.cozy.files"
		index.Index(docs[i].ID, docs[i])
	}

}

func FillAlbumPhotosIndex(index bleve.Index, instance *instance.Instance) {
	var docs []file
	GetFileDocs(index, instance, "io.cozy.photos.albums", &docs)
	fmt.Println(docs)
	for i := range docs {
		docs[i].DocType = "io.cozy.photos.albums"
		index.Index(docs[i].ID, docs[i])
	}

}

func GetFileDocs(index bleve.Index, instance *instance.Instance, docType string, docs *[]file) {
	req := &couchdb.AllDocsRequest{Limit: 100}
	err := couchdb.GetAllDocs(instance, docType, req, docs)
	if err != nil {
		fmt.Println("Error on unmarshall: %s\n", err)
	}
}

func QueryIndex(index bleve.Index, instance *instance.Instance, queryString string) {
	query := bleve.NewQueryStringQuery(queryString)
	search := bleve.NewSearchRequest(query)
	searchResults, err := index.Search(search)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(searchResults.String())

	var fetched couchdb.JSONDoc
	for _, result := range searchResults.Hits {
		couchdb.GetDoc(instance, "io.cozy.files", result.ID, &fetched)
		fmt.Println(fetched)
	}
}
