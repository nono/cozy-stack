package search

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/blevesearch/bleve"
	"github.com/blevesearch/bleve/analysis/lang/en"
	"github.com/blevesearch/bleve/analysis/lang/fr"
	"github.com/cozy/cozy-stack/pkg/consts"
	"github.com/cozy/cozy-stack/pkg/couchdb"
	"github.com/cozy/cozy-stack/pkg/instance"
	"github.com/cozy/cozy-stack/web/middlewares"
	"github.com/labstack/echo"
)

func getFilesIndex(ins *instance.Instance) (bleve.Index, error) {
	doctype := consts.Files
	nameFieldMapping := bleve.NewTextFieldMapping()
	nameFieldMapping.Analyzer = en.AnalyzerName
	docMapping := bleve.NewDocumentMapping()
	docMapping.AddFieldMappingsAt("name", nameFieldMapping)
	mapping := bleve.NewIndexMapping()
	mapping.DefaultType = doctype
	mapping.AddDocumentMapping(doctype, docMapping)

	idx, err := bleve.NewMemOnly(mapping) // TODO in-memory is just for testing
	if err != nil {
		return nil, err
	}
	idx.SetName(doctype)

	var docs []map[string]interface{}
	req := &couchdb.AllDocsRequest{Limit: 1000} // TODO index all docs
	if err = couchdb.GetAllDocs(ins, doctype, req, &docs); err != nil {
		return nil, err
	}

	for _, doc := range docs {
		if id, ok := doc["_id"].(string); ok {
			idx.Index(id, doc)
		}
	}

	return idx, nil
}

func getContactsIndex(ins *instance.Instance) (bleve.Index, error) {
	doctype := consts.Contacts
	mapping := bleve.NewIndexMapping()
	mapping.DefaultType = doctype
	mapping.DefaultAnalyzer = fr.AnalyzerName

	idx, err := bleve.NewMemOnly(mapping) // TODO in-memory is just for testing
	if err != nil {
		return nil, err
	}
	idx.SetName(doctype)

	var docs []map[string]interface{}
	req := &couchdb.AllDocsRequest{Limit: 1000} // TODO index all docs
	if err = couchdb.GetAllDocs(ins, doctype, req, &docs); err != nil {
		return nil, err
	}

	for _, doc := range docs {
		if id, ok := doc["_id"].(string); ok {
			idx.Index(id, doc)
		}
	}

	return idx, nil
}

func getIndex(ins *instance.Instance, doctype string) (bleve.Index, error) {
	switch doctype {
	case consts.Files:
		return getFilesIndex(ins)
	case consts.Contacts:
		return getContactsIndex(ins)
	}
	return nil, errors.New("Only io.cozy.files and io.cozy.contacts can be searched currently")
}

type response struct {
	Hits  []interface{} `json:"hits"`
	Total uint64        `json:"total"`
}

// curl "http://cozy.tools:8080/search/io.cozy.files?q=Demo" | jq .
func search(c echo.Context) error {
	doctype := c.Param("doctype")

	q := c.QueryParam("q")
	if q == "" {
		return errors.New("q parameter is mandatory")
	}

	ins := middlewares.GetInstance(c)
	idx, err := getIndex(ins, doctype)
	if err != nil {
		return err
	}

	query := bleve.NewQueryStringQuery(q)
	request := bleve.NewSearchRequest(query)
	results, err := idx.Search(request)
	if err != nil {
		return err
	}

	var hits []interface{}
	if len(results.Hits) > 0 {
		ids := make([]string, len(results.Hits))
		for i, h := range results.Hits {
			ids[i] = h.ID
		}
		keys, err := json.Marshal(ids)
		if err != nil {
			return err
		}
		find := &couchdb.AllDocsRequest{
			Keys: string(keys),
		}
		if err := couchdb.GetAllDocs(ins, doctype, find, &hits); err != nil {
			return err
		}
	}

	response := response{
		Hits:  hits,
		Total: results.Total,
	}

	// TODO facets, highlighting, paginatination
	// TODO authorization
	// TODO improve errors
	// TODO use JSON-API
	return c.JSON(http.StatusOK, response)
}

// Routes sets the routing for the search service
func Routes(router *echo.Group) {
	router.GET("/:doctype", search)
}
