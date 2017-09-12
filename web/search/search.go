package search

import (
	"errors"
	"net/http"

	"github.com/blevesearch/bleve"
	"github.com/cozy/cozy-stack/pkg/consts"
	"github.com/cozy/cozy-stack/pkg/couchdb"
	"github.com/cozy/cozy-stack/pkg/instance"
	"github.com/cozy/cozy-stack/web/middlewares"
	"github.com/labstack/echo"
)

func getFilesIndex(ins *instance.Instance) (bleve.Index, error) {
	doctype := consts.Files
	nameFieldMapping := bleve.NewTextFieldMapping()
	nameFieldMapping.Analyzer = "en"
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
	docMapping := bleve.NewDocumentMapping()
	docMapping.DefaultAnalyzer = "en"
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

func getIndex(ins *instance.Instance, doctype string) (bleve.Index, error) {
	switch doctype {
	case consts.Files:
		return getFilesIndex(ins)
	case consts.Contacts:
		return getContactsIndex(ins)
	}
	return nil, errors.New("Only io.cozy.files and io.cozy.contacts can be searched currently")
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
	request.Fields = []string{"*"}
	results, err := idx.Search(request)
	if err != nil {
		return err
	}

	// TODO authorization
	// TODO improve errors
	// TODO use JSON-API
	return c.JSON(http.StatusOK, results)
}

// Routes sets the routing for the search service
func Routes(router *echo.Group) {
	router.GET("/:doctype", search)
}
