package index

import (
	"fmt"
	"time"

	"github.com/blevesearch/bleve"
	// "github.com/blevesearch/bleve/mapping"
	"github.com/cozy/cozy-stack/pkg/consts"
	"github.com/cozy/cozy-stack/pkg/couchdb"
	"github.com/cozy/cozy-stack/pkg/instance"
	"github.com/cozy/cozy-stack/pkg/realtime"
	"github.com/cozy/cozy-stack/pkg/vfs"
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

type photoAlbum struct {
	ID        string    `json:"_id"`
	Rev       string    `json:"_rev"`
	CreatedAt time.Time `json:"created_at"`
	Name      string    `json:"name"`
	DocType   string    `json:"docType"`
}

// var typeMap map[string]interface{}

var mapIndexType map[string]string
var indexAlias bleve.Index
var inst *instance.Instance

type fileWithContent struct {
	realtime.Doc
	Content string `json:"content"`
}

func StartIndex(instance *instance.Instance) error {
	inst = instance

	mapIndexType = map[string]string{
		"bleve/photo.albums.bleve":  consts.PhotosAlbums,
		"bleve/file.bleve":          consts.Files,
		"bleve/bank.accounts.bleve": "io.cozy.bank.accounts", // TODO : check why it doesn't exist in consts
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
				doc := ev.Doc
				if doc.DocType() == consts.Files {
					content, err := vfs.IndexableContent(instance.VFS(), doc.ID())
					if err != nil {
						instance.Logger().WithField("nspace", "index").
							Errorf("Error on IndexableContent: %s", err)
					}
					doc = &fileWithContent{doc, content}
				}
				(*originalIndex).Index(doc.ID(), doc)
				fmt.Printf("%#v", doc)
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

	// Create it if it doesn't exist
	if err1 == bleve.ErrorIndexPathDoesNotExist {
		fmt.Printf("Creating new index %s...\n", indexPath)
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

	fmt.Println("found existing Index")
	return i, nil
}

func FillIndex(index bleve.Index, docType string) {

	// Which solution to use ?
	// Either a common struct (such as JSONDoc) or a struct by type of document ?

	// 	// Specified struct

	// var docsFile []file
	// var docsPhotoAlbum []photoAlbum
	// if docType == "io.cozy.photos.albums" {
	// 	GetFileDocs(docType, &docsPhotoAlbum)
	// 	for i := range docsPhotoAlbum {
	// 		docsPhotoAlbum[i].DocType = docType
	// 		index.Index(docsPhotoAlbum[i].ID, docsPhotoAlbum[i])
	// 	}
	// } else {
	// 	GetFileDocs(docType, &docsFile)
	// 	for i := range docsFile {
	// 		docsFile[i].DocType = docType
	// 		index.Index(docsFile[i].ID, docsFile[i])
	// 	}
	// }

	// 	// Common struct

	// // Indexation Time
	// start := time.Now()
	// var docs []couchdb.JSONDoc
	// GetFileDocs(docType, &docs)
	// for i := range docs {
	// 	docs[i].M["DocType"] = docType
	// 	index.Index(docs[i].ID(), docs[i].M)
	// }
	// end := time.Since(start)
	// fmt.Println(docType, " indexing time: ", end, " for ", len(docs), " documents")

	// Indexation Batch Time
	start := time.Now()
	var docs []couchdb.JSONDoc
	batch := index.NewBatch()
	GetFileDocs(docType, &docs)
	for i := range docs {
		docs[i].M["DocType"] = docType
		batch.Index(docs[i].ID(), docs[i].M)
	}
	index.Batch(batch)
	end := time.Since(start)
	fmt.Println(docType, " indexing time: ", end, " for ", len(docs), " documents")

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
	searchRequest := bleve.NewSearchRequest(query)

	// Addings Facets
	// docTypes facet
	searchRequest.AddFacet("docTypes", bleve.NewFacetRequest("DocType", 3))
	// created facet
	var cutOffDate = time.Now().Add(-7 * 24 * time.Hour)
	createdFacet := bleve.NewFacetRequest("created_at", 2)
	createdFacet.AddDateTimeRange("old", time.Unix(0, 0), cutOffDate)
	createdFacet.AddDateTimeRange("new", cutOffDate, time.Unix(9999999999, 9999999999)) //check how many 9 needed
	searchRequest.AddFacet("created", createdFacet)

	searchResults, err := indexAlias.Search(searchRequest)
	if err != nil {
		fmt.Printf("Error on querying: %s", err)
		return fetched, err
	}
	fmt.Printf(searchResults.String())

	for _, dateRange := range searchResults.Facets["created"].DateRanges {
		fmt.Printf("\t%s(%d)\n", dateRange.Name, dateRange.Count)
	}

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
