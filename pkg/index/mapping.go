package index

import (
	"github.com/blevesearch/bleve"
	"github.com/blevesearch/bleve/mapping"
	// "github.com/blevesearch/bleve/analysis/analyzer/simple" // Might be useful to check for other Analyzers (maybe make one ourselves)
)

func AddTypeMapping(indexMapping *mapping.IndexMappingImpl, docType string) {
	switch docType {
	case "io.cozy.photos.albums":
		indexMapping = AddPhotoAlbumMapping(indexMapping)
		break
	case "io.cozy.files":
		indexMapping = AddFileMapping(indexMapping)
		break
	case "io.cozy.bank.accounts":
		indexMapping = AddBankAccountMapping(indexMapping)
		break
	}
	indexMapping.TypeField = "DocType"
}

func AddPhotoAlbumMapping(indexMapping *mapping.IndexMappingImpl) *mapping.IndexMappingImpl {
	photosAlbumMapping := bleve.NewDocumentMapping()

	englishTextFieldMapping := bleve.NewTextFieldMapping()
	// englishTextFieldMapping.Analyzer = "en"
	// englishTextFieldMapping.IncludeInAll = true

	photosAlbumMapping.AddFieldMappingsAt("name", englishTextFieldMapping)

	indexMapping.AddDocumentMapping("io.cozy.photos.albums", photosAlbumMapping)

	return indexMapping
}

func AddFileMapping(indexMapping *mapping.IndexMappingImpl) *mapping.IndexMappingImpl {
	FileMapping := bleve.NewDocumentMapping()

	englishTextFieldMapping := bleve.NewTextFieldMapping()
	// englishTextFieldMapping.Index = false
	// englishTextFieldMapping.Analyzer = "en"
	// englishTextFieldMapping.IncludeInAll = true

	dateMapping := bleve.NewDateTimeFieldMapping()

	FileMapping.AddFieldMappingsAt("name", englishTextFieldMapping)
	FileMapping.AddFieldMappingsAt("created_at", dateMapping)
	FileMapping.AddFieldMappingsAt("updated_at", dateMapping)
	FileMapping.AddFieldMappingsAt("tags", englishTextFieldMapping)
	// TODO: check tag mapping (knowing it's an array)

	indexMapping.AddDocumentMapping("io.cozy.files", FileMapping)

	return indexMapping
}

func AddBankAccountMapping(indexMapping *mapping.IndexMappingImpl) *mapping.IndexMappingImpl {
	BankAccountMapping := bleve.NewDocumentMapping()

	englishTextFieldMapping := bleve.NewTextFieldMapping()
	englishTextFieldMapping.Analyzer = "en"
	englishTextFieldMapping.IncludeInAll = true

	simpleMapping := bleve.NewTextFieldMapping()
	// Todo: check it is actually without analyzer

	numberMapping := bleve.NewNumericFieldMapping()

	BankAccountMapping.AddFieldMappingsAt("label", englishTextFieldMapping)
	BankAccountMapping.AddFieldMappingsAt("institutionLabel", englishTextFieldMapping)
	BankAccountMapping.AddFieldMappingsAt("balance", numberMapping)
	BankAccountMapping.AddFieldMappingsAt("type", englishTextFieldMapping)
	BankAccountMapping.AddFieldMappingsAt("number", simpleMapping)
	BankAccountMapping.AddFieldMappingsAt("iban", simpleMapping)
	BankAccountMapping.AddFieldMappingsAt("serviceID", numberMapping) // Todo : test when is undefined

	indexMapping.AddDocumentMapping("io.cozy.bank.accounts", BankAccountMapping)

	return indexMapping
}

// Todo: io.cozy.bank.operations
