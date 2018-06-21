package vfs

import "io/ioutil"

// IndexableContent returns the content of the file with the given ID that can
// be indexed for a fulltext search.
func IndexableContent(fs VFS, id string) (string, error) {
	doc, err := fs.FileByID(id)
	if err != nil {
		return "", err
	}
	switch doc.Mime {
	case "text/plain":
		reader, err := fs.OpenFile(doc)
		if err != nil {
			return "", err
		}
		defer reader.Close()
		content, err := ioutil.ReadAll(reader)
		return string(content), err
		// TODO files with other doctypes like PDFs
	}
	return "", nil
}
