package index

import (
	"fmt"
	"time"

	"github.com/blevesearch/bleve"
	"github.com/blevesearch/bleve/mapping"
	"github.com/cozy/cozy-stack/pkg/couchdb"
	"github.com/cozy/cozy-stack/pkg/instance"
)

func StartIndex() (bleve.Index, error) {

	indexMapping := bleve.NewIndexMapping()
	indexMapping = addPhotoAlbumMapping(indexMapping)
	indexMapping = addFileMapping(indexMapping)
	indexMapping = addBankAccountMapping(indexMapping)
	indexMapping.TypeField = "DocType"

	return OpenIndex(indexMapping)

	// test(index)
}

func OpenIndex(indexMapping *mapping.IndexMappingImpl) (bleve.Index, error) {
	return bleve.NewMemOnly(indexMapping)
	// TODO: implement real index (not mem only)
}

func FillIndex(index bleve.Index, instance *instance.Instance) {

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
		// Metadata   struct {
		// 	Datetime         time.Time `json:"datetime"`
		// 	ExtractorVersion int       `json:"extractor_version"`
		// 	Height           int       `json:"height"`
		// 	Width            int       `json:"width"`
		// } `json:"metadata"`
		DocType string `json:"docType"`
	}

	// var docs []map[string]interface{}
	var docs []file
	req := &couchdb.AllDocsRequest{Limit: 100}
	err := couchdb.GetAllDocs(instance, "io.cozy.files", req, &docs)
	if err != nil {
		fmt.Println("Error on unmarshall: %s\n", err)
	}

	// batch := index.NewBatch()
	for i := range docs {
		// batch.Index(docs[i]["_id"], docs[i])
		// batch.Index(docs[i]["_id"].(string), docs[i])
		docs[i].DocType = "io.cozy.files"
		index.Index(docs[i].ID, docs[i])
		// fmt.Print(i)
		// fmt.Print(" - ")
		// fmt.Println(docs[i])
		fmt.Print(docs[i].ID)
		fmt.Print(" - ")
		fmt.Println(docs[i].Name)
	}

	index.Index("testid",
		file{
			ID:         "id",
			Rev:        "nil",
			Type:       "nil",
			Name:       "administrative qwant",
			DirID:      "nil",
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
			Size:       "nil",
			Md5Sum:     "nil",
			Mime:       "nil",
			Class:      "nil",
			Executable: true,
			Trashed:    true,
			Tags:       nil,
			DocType:    "io.cozy.files",
		})

	// test(index)

}

func QueryIndex(index bleve.Index, queryString string) {
	query := bleve.NewQueryStringQuery(queryString)
	search := bleve.NewSearchRequest(query)
	searchResults, err := index.Search(search)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(searchResults)
}

func test(index bleve.Index) {
	type photosAlbum struct {
		Name    string `json:"name"`
		DocType string `json:"docType"`
	}

	type file struct {
		Name       string   `json:"name"`
		Created_At string   `json:"created_at"`
		Updated_At string   `json:"updated_at"`
		Tags       []string `json:"tags"`
		DocType    string   `json:"docType"`
	}

	type bankAccount struct {
		Label            string  `json:"label"`
		InstitutionLabel string  `json:"institutionLabel"`
		Balance          float64 `json:"balance"`
		Type             string  `json:"type"`
		Number           string  `json:"number"`
		Iban             string  `json:"iban"`
		ServiceID        float64 `json:"serviceID"`
		DocType          string  `json:"docType"`
	}

	data1 := photosAlbum{
		Name:    "qwant qwant qwant",
		DocType: "io.cozy.photos.albums",
	}

	data2 := file{
		Name:       "Rapport des plages horaires",
		Created_At: "2017-04-22T01:00:00-05:00",
		Updated_At: "2019-05-22T01:00:00-05:00",
		Tags:       []string{"zoo", "park"},
		DocType:    "io.cozy.files",
	}

	data3 := file{
		Name:       "Rapport TN10 qwant",
		Created_At: "2018-04-22T01:00:00-05:00",
		Updated_At: "2020-05-22T01:00:00-05:00",
		Tags:       []string{"zoo", "park"},
		DocType:    "io.cozy.files",
	}

	data4 := bankAccount{
		Label:            "Livret Dévelop. Durable (x1337)",
		InstitutionLabel: "Société Générale Qwant (Particuliers)",
		Balance:          1337.73,
		Type:             "Savings",
		Number:           "03791 00048085818",
		Iban:             "03791 00048085818",
		ServiceID:        133356,
		DocType:          "io.cozy.bank.accounts",
	}

	index.Index("id1", data1)
	index.Index("id2", data2)
	index.Index("id3", data3)
	index.Index("id4", data4)

	// oldQueryIndex(index, "updated_at:>\"2018-01-22T01:00:00-05:00\" qwant name:qwant^5")
}

func oldQueryIndex(index bleve.Index, queryString string) {
	// Simple Match query
	// query := bleve.NewMatchQuery("plage")

	// Date Range query
	// start, _ := time.Parse(time.RFC3339, "2018-01-22T01:00:00-05:00")
	// end, _ := time.Parse(time.RFC3339, "2019-01-22T01:00:00-05:00")
	// query := bleve.NewDateRangeQuery(start, end) // bleve.NewMatchQuery("created_at:>'2018-01-01'")

	// Numeric Range query
	// start := float64(1000)
	// end := float64(2000)
	// query := bleve.NewNumericRangeQuery(&start, &end)

	// Complex String query
	// query := bleve.NewQueryStringQuery("updated_at:>\"2018-01-22T01:00:00-05:00\" plage name:plage^5")

	query := bleve.NewQueryStringQuery(queryString)
	search := bleve.NewSearchRequest(query)
	searchResults, err := index.Search(search)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(searchResults)
}
