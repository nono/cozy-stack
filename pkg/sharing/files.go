package sharing

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/cozy/cozy-stack/client/request"
	"github.com/cozy/cozy-stack/pkg/consts"
	"github.com/cozy/cozy-stack/pkg/couchdb"
	"github.com/cozy/cozy-stack/pkg/crypto"
	"github.com/cozy/cozy-stack/pkg/instance"
	"github.com/cozy/cozy-stack/pkg/vfs"
	multierror "github.com/hashicorp/go-multierror"
)

// isTrashed returns true for a file or folder inside the trash
func isTrashed(doc couchdb.JSONDoc) bool {
	if doc.Type != consts.Files {
		return false
	}
	if doc.Get("type") == consts.FileType {
		return doc.Get("trashed") == true
	}
	return strings.HasPrefix(doc.Get("path").(string), vfs.TrashDirName+"/")
}

// MakeXorKey generates a key for transforming the file identifiers
func MakeXorKey() []byte {
	random := crypto.GenerateRandomBytes(8)
	result := make([]byte, 2*len(random))
	for i, val := range random {
		result[2*i] = val & 0xf
		result[2*i+1] = val >> 4
	}
	return result
}

// XorID transforms the identifier of a file to a new identifier, in a
// reversible way: it makes a XOR on the hexadecimal characters
func XorID(id string, key []byte) string {
	l := len(key)
	buf := []byte(id)
	for i, c := range buf {
		switch {
		case '0' <= c && c <= '9':
			c = (c - '0') ^ key[i%l]
		case 'a' <= c && c <= 'f':
			c = (c - 'a' + 10) ^ key[i%l]
		case 'A' <= c && c <= 'F':
			c = (c - 'A' + 10) ^ key[i%l]
		default:
			continue
		}
		if c < 10 {
			buf[i] = c + '0'
		} else {
			buf[i] = (c - 10) + 'a'
		}
	}
	return string(buf)
}

// SortFilesToSent sorts the files slice that will be sent in bulk_docs:
// - directories must come before files (if a file is created in a new
//   directory, we must create directory before the file)
// - directories are sorted by increasing depth (if a sub-folder is created
//   in a new directory, we must create the parent before the child)
func (s *Sharing) SortFilesToSent(files []map[string]interface{}) {
	sort.SliceStable(files, func(i, j int) bool {
		a, b := files[i], files[j]
		if a["type"] == consts.FileType {
			return false
		}
		if b["type"] == consts.FileType {
			return true
		}
		if removed, ok := a["_deleted"].(bool); ok && removed {
			return true
		}
		if removed, ok := b["_deleted"].(bool); ok && removed {
			return false
		}
		p, ok := a["path"].(string)
		if !ok {
			return true
		}
		q, ok := b["path"].(string)
		if !ok {
			return false
		}
		return strings.Count(p, "/") < strings.Count(q, "/")
	})
}

// TransformFileToSent transforms an io.cozy.files document before sending it
// to another cozy instance:
// - its identifier is XORed
// - its dir_id is XORed or removed
// - the path is removed (directory only)
//
// ruleIndexes is a map of "doctype-docid" -> rule index
func (s *Sharing) TransformFileToSent(doc map[string]interface{}, xorKey []byte, ruleIndex int) map[string]interface{} {
	if doc["type"] == consts.DirType {
		delete(doc, "path")
	}
	id := doc["_id"].(string)
	doc["_id"] = XorID(id, xorKey)
	dir, ok := doc["dir_id"].(string)
	if !ok {
		return doc
	}
	rule := s.Rules[ruleIndex]
	var noDirID bool
	if rule.Selector == couchdb.SelectorReferencedBy {
		noDirID = true
		if refs, ok := doc[couchdb.SelectorReferencedBy].([]interface{}); ok {
			kept := make([]interface{}, 0)
			for _, ref := range refs {
				if r, ok := ref.(map[string]interface{}); ok {
					v := r["type"].(string) + "/" + r["id"].(string)
					for _, val := range rule.Values {
						if val == v {
							kept = append(kept, ref)
							break
						}
					}
				}
			}
			doc[couchdb.SelectorReferencedBy] = kept
		}
	} else {
		noDirID = false
		delete(doc, couchdb.SelectorReferencedBy)
	}
	if !noDirID {
		for _, v := range rule.Values {
			if v == dir {
				noDirID = true
				break
			}
		}
	}
	if noDirID {
		delete(doc, "dir_id")
	} else {
		doc["dir_id"] = XorID(dir, xorKey)
	}
	return doc
}

// EnsureSharedWithMeDir returns the shared-with-me directory, and create it if
// it doesn't exist
func EnsureSharedWithMeDir(inst *instance.Instance) (*vfs.DirDoc, error) {
	fs := inst.VFS()
	dir, _, err := fs.DirOrFileByID(consts.SharedWithMeDirID)
	if err != nil && err != os.ErrNotExist {
		return nil, err
	}

	if dir == nil {
		name := inst.Translate("Tree Shared with me")
		dir, err = vfs.NewDirDocWithPath(name, consts.RootDirID, "/", nil)
		dir.DocID = consts.SharedWithMeDirID
		if err != nil {
			return nil, err
		}
		if err = fs.CreateDir(dir); err != nil {
			return nil, err
		}
		return dir, nil
	}

	if dir.RestorePath != "" {
		_, err = vfs.RestoreDir(fs, dir)
		if err != nil {
			return nil, err
		}
		children, err := fs.DirBatch(dir, &couchdb.SkipCursor{})
		if err != nil {
			return nil, err
		}
		for _, child := range children {
			d, f := child.Refine()
			if d != nil {
				_, err = vfs.TrashDir(fs, d)
			} else {
				_, err = vfs.TrashFile(fs, f)
			}
			if err != nil {
				return nil, err
			}
		}
	}

	return dir, nil
}

// CreateDirForSharing creates the directory where files for this sharing will
// be put. This directory will be initially inside the Shared with me folder.
func (s *Sharing) CreateDirForSharing(inst *instance.Instance, rule *Rule) error {
	parent, err := EnsureSharedWithMeDir(inst)
	if err != nil {
		return err
	}
	fs := inst.VFS()
	dir, err := vfs.NewDirDocWithParent(rule.Title, parent, []string{"from-sharing-" + s.SID})
	parts := strings.Split(rule.Values[0], "/")
	dir.DocID = parts[len(parts)-1]
	if err != nil {
		return err
	}
	dir.AddReferencedBy(couchdb.DocReference{
		ID:   s.SID,
		Type: consts.Sharings,
	})
	return fs.CreateDir(dir)
}

// AddReferenceForSharingDir adds a reference to the sharing on the sharing directory
func (s *Sharing) AddReferenceForSharingDir(inst *instance.Instance, rule *Rule) error {
	fs := inst.VFS()
	parts := strings.Split(rule.Values[0], "/")
	dir, err := fs.DirByID(parts[len(parts)-1])
	if err != nil {
		return err
	}
	for _, ref := range dir.ReferencedBy {
		if ref.Type == consts.Sharings && ref.ID == s.SID {
			return nil
		}
	}
	olddoc := dir.Clone().(*vfs.DirDoc)
	dir.AddReferencedBy(couchdb.DocReference{
		ID:   s.SID,
		Type: consts.Sharings,
	})
	return fs.UpdateDirDoc(olddoc, dir)
}

// GetSharingDir returns the directory used by this sharing for putting files
// and folders that have no dir_id.
func (s *Sharing) GetSharingDir(inst *instance.Instance) (*vfs.DirDoc, error) {
	key := []string{consts.Sharings, s.SID}
	end := []string{key[0], key[1], couchdb.MaxString}
	req := &couchdb.ViewRequest{
		StartKey:    key,
		EndKey:      end,
		IncludeDocs: true,
	}
	var res couchdb.ViewResponse
	err := couchdb.ExecView(inst, consts.FilesReferencedByView, req, &res)
	if err != nil || len(res.Rows) == 0 {
		inst.Logger().WithField("nspace", "sharing").
			Warnf("Sharing dir not found: %v (%s)", err, s.SID)
		return nil, ErrInternalServerError
	}
	return inst.VFS().DirByID(res.Rows[0].ID)
}

// GetNoLongerSharedDir returns the directory used for files and folders that
// are removed from a sharing, but are still used via a reference. It the
// directory does not exist, it is created.
func (s *Sharing) GetNoLongerSharedDir(inst *instance.Instance) (*vfs.DirDoc, error) {
	fs := inst.VFS()
	dir, _, err := fs.DirOrFileByID(consts.NoLongerSharedDirID)
	if err != nil && err != os.ErrNotExist {
		return nil, err
	}

	if dir == nil {
		parent, errp := EnsureSharedWithMeDir(inst)
		if errp != nil {
			return nil, errp
		}
		name := inst.Translate("Tree No longer shared")
		dir, err = vfs.NewDirDocWithParent(name, parent, nil)
		dir.DocID = consts.NoLongerSharedDirID
		if err != nil {
			return nil, err
		}
		if err = fs.CreateDir(dir); err != nil {
			return nil, err
		}
		return dir, nil
	}

	if dir.RestorePath != "" {
		_, err = vfs.RestoreDir(fs, dir)
		if err != nil {
			return nil, err
		}
		children, err := fs.DirBatch(dir, &couchdb.SkipCursor{})
		if err != nil {
			return nil, err
		}
		for _, child := range children {
			d, f := child.Refine()
			if d != nil {
				_, err = vfs.TrashDir(fs, d)
			} else {
				_, err = vfs.TrashFile(fs, f)
			}
			if err != nil {
				return nil, err
			}
		}
	}

	return dir, nil
}

// GetFolder returns informations about a folder (with XORed IDs)
func (s *Sharing) GetFolder(inst *instance.Instance, m *Member, xoredID string) (map[string]interface{}, error) {
	creds := s.FindCredentials(m)
	if creds == nil {
		return nil, ErrInvalidSharing
	}
	dirID := XorID(xoredID, creds.XorKey)
	ref := &SharedRef{}
	err := couchdb.GetDoc(inst, consts.Shared, consts.Files+"/"+dirID, ref)
	if err != nil {
		return nil, err
	}
	info, ok := ref.Infos[s.SID]
	if !ok || info.Removed {
		return nil, ErrFolderNotFound
	}
	dir, err := inst.VFS().DirByID(dirID)
	if err != nil {
		return nil, err
	}
	doc := s.TransformFileToSent(dirToJSONDoc(dir).M, creds.XorKey, info.Rule)
	return doc, nil
}

// ApplyBulkFiles takes a list of documents for the io.cozy.files doctype and
// will apply changes to the VFS according to those documents.
func (s *Sharing) ApplyBulkFiles(inst *instance.Instance, docs DocsList) error {
	var errm error
	fs := inst.VFS()

	for _, target := range docs {
		id, ok := target["_id"].(string)
		if !ok {
			errm = multierror.Append(errm, ErrMissingID)
			continue
		}
		ref := &SharedRef{}
		err := couchdb.GetDoc(inst, consts.Shared, consts.Files+"/"+id, ref)
		if err != nil {
			if !couchdb.IsNotFoundError(err) {
				inst.Logger().WithField("nspace", "replicator").
					Debugf("Error on finding doc of bulk files: %s", err)
				errm = multierror.Append(errm, err)
				continue
			}
			ref = nil
		}
		var infos SharedInfo
		if ref != nil {
			infos, ok = ref.Infos[s.SID]
			if !ok {
				errm = multierror.Append(errm, ErrSafety)
				continue
			}
		}
		dir, file, err := fs.DirOrFileByID(id)
		if err != nil && err != os.ErrNotExist {
			inst.Logger().WithField("nspace", "replicator").
				Debugf("Error on finding ref of bulk files: %s", err)
			errm = multierror.Append(errm, err)
			continue
		}
		if _, ok := target["_deleted"]; ok {
			if ref == nil || (dir == nil && file == nil) {
				continue
			}
			gen := ref.Revisions.Generation()
			if gen >= RevGeneration(target["_rev"].(string)) {
				// Either the file/dir is already in the trash, or the not
				// trashed version should win => keep the file/dir as it is
				continue
			}
			if dir != nil {
				err = s.TrashDir(inst, dir)
			} else {
				err = s.TrashFile(inst, file, &s.Rules[infos.Rule])
			}
			if err != nil {
				errm = multierror.Append(errm, err)
			}
		} else if dir == nil {
			err = s.CreateDir(inst, target)
			if err != nil {
				errm = multierror.Append(errm, err)
			}
		} else if ref == nil {
			errm = multierror.Append(errm, ErrSafety)
			continue
		} else {
			err = s.UpdateDir(inst, target, dir)
			if err != nil {
				errm = multierror.Append(errm, err)
			}
		}
	}
	return nil
}

func removeReferencesFromRule(file *vfs.FileDoc, rule *Rule) {
	if rule.Selector != couchdb.SelectorReferencedBy {
		return
	}
	refs := file.ReferencedBy[:0]
	for _, ref := range file.ReferencedBy {
		if !rule.hasReferencedBy(ref) {
			refs = append(refs, ref)
		}
	}
	file.ReferencedBy = refs
}

func buildReferencedBy(target, file *vfs.FileDoc, rule *Rule) []couchdb.DocReference {
	refs := make([]couchdb.DocReference, 0)
	if file != nil {
		for _, ref := range file.ReferencedBy {
			if !rule.hasReferencedBy(ref) {
				refs = append(refs, ref)
			}
		}
	}
	for _, ref := range target.ReferencedBy {
		if rule.hasReferencedBy(ref) {
			refs = append(refs, ref)
		}
	}
	return refs
}

func copySafeFieldsToFile(target, file *vfs.FileDoc) {
	file.Tags = make([]string, len(target.Tags))
	copy(file.Tags, target.Tags)
	file.CreatedAt = target.CreatedAt
	file.UpdatedAt = target.UpdatedAt
	file.Mime = target.Mime
	file.Class = target.Class
	file.Executable = target.Executable
}

func copySafeFieldsToDir(target map[string]interface{}, dir *vfs.DirDoc) {
	if tags, ok := target["tags"].([]interface{}); ok {
		dir.Tags = make([]string, 0, len(tags))
		for _, tag := range tags {
			if t, ok := tag.(string); ok {
				dir.Tags = append(dir.Tags, t)
			}
		}
	}
	if created, ok := target["created_at"].(string); ok {
		if at, err := time.Parse(time.RFC3339Nano, created); err == nil {
			dir.CreatedAt = at
		}
	}
	if updated, ok := target["updated_at"].(string); ok {
		if at, err := time.Parse(time.RFC3339Nano, updated); err == nil {
			dir.UpdatedAt = at
		}
	}
}

// resolveConflictSamePath is used when two files/folders are in conflict
// because they have the same path. To resolve the conflict, we take the
// file/folder with the greatest id as the winner and rename the other.
// If the winner is the new file/folder from the other cozy, this function
// rename the local file/folder and let the caller retry its operation.
// If the winner is the local file/folder, this function returns the new name
// and let the caller do its operation with the new name (the caller should
// create a dummy revision to let the other cozy know of the renaming).
func resolveConflictSamePath(inst *instance.Instance, id, pth string) (string, error) {
	fs := inst.VFS()
	d, f, err := fs.DirOrFileByPath(pth)
	if err != nil {
		return "", err
	}
	name := fmt.Sprintf("%s - conflict - %d", pth, time.Now().Unix())
	if d != nil {
		if d.DocID > id {
			return name, nil
		}
		old := d.Clone().(*vfs.DirDoc)
		d.DocName = name
		return "", fs.UpdateDirDoc(old, d)
	}
	if f.DocID > id {
		return name, nil
	}
	old := f.Clone().(*vfs.FileDoc)
	f.DocName = name
	return "", fs.UpdateFileDoc(old, f)
}

//getDirDocFromInstance fetches informations about a directory from the given
//member of the sharing.
func (s *Sharing) getDirDocFromInstance(inst *instance.Instance, m *Member, creds *Credentials, dirID string) (*vfs.DirDoc, error) {
	u, err := url.Parse(m.Instance)
	if err != nil {
		return nil, ErrInvalidSharing
	}
	opts := &request.Options{
		Method: http.MethodGet,
		Scheme: u.Scheme,
		Domain: u.Host,
		Path:   "/sharings/" + s.SID + "/io.cozy.files/" + dirID,
		Headers: request.Headers{
			"Accept":        "application/json",
			"Authorization": "Bearer " + creds.AccessToken.AccessToken,
		},
	}
	res, err := request.Req(opts)
	if err != nil {
		return nil, err
	}
	if res.StatusCode/100 == 4 {
		res.Body.Close()
		res, err = RefreshToken(inst, s, m, creds, opts, nil)
		if err != nil {
			return nil, err
		}
	}
	defer res.Body.Close()
	if res.StatusCode/100 == 5 {
		return nil, ErrInternalServerError
	}
	var doc *vfs.DirDoc
	if err = json.NewDecoder(res.Body).Decode(&doc); err != nil {
		return nil, err
	}
	return doc, nil
}

// getDirDocFromNetwork fetches informations about a directory from the other
// cozy instances of this sharing.
func (s *Sharing) getDirDocFromNetwork(inst *instance.Instance, dirID string) (*vfs.DirDoc, error) {
	if !s.Owner {
		return s.getDirDocFromInstance(inst, &s.Members[0], &s.Credentials[0], dirID)
	}
	for i := range s.Credentials {
		doc, err := s.getDirDocFromInstance(inst, &s.Members[i+1], &s.Credentials[i], dirID)
		if err == nil {
			return doc, nil
		}
	}
	return nil, ErrFolderNotFound
}

// recreateParent is used when a file or folder is added by a cozy, and sent to
// this instance, but its parent directory was trashed and deleted on this
// cozy. To resolve the conflict, this instance will fetch informations from
// the other instance about the parent directory and will recreate it. It can
// be necessary to recurse if there were several levels of directories deleted.
func (s *Sharing) recreateParent(inst *instance.Instance, dirID string) (*vfs.DirDoc, error) {
	doc, err := s.getDirDocFromNetwork(inst, dirID)
	if err != nil {
		return nil, err
	}
	fs := inst.VFS()
	var parent *vfs.DirDoc
	if doc.DirID == "" {
		parent, err = s.GetSharingDir(inst)
	} else {
		parent, err = fs.DirByID(doc.DirID)
		if err == os.ErrNotExist {
			parent, err = s.recreateParent(inst, doc.DirID)
		}
	}
	if err != nil {
		return nil, err
	}
	doc.DirID = parent.DocID
	doc.Fullpath = path.Join(parent.Fullpath, doc.DocName)
	doc.SetRev("")
	err = fs.CreateDir(doc)
	if err != nil {
		return nil, err
	}
	return doc, nil
}

// CreateDir creates a directory on this cozy to reflect a change on another
// cozy instance of this sharing.
func (s *Sharing) CreateDir(inst *instance.Instance, target map[string]interface{}) error {
	name, ok := target["name"].(string)
	if !ok {
		inst.Logger().WithField("nspace", "replicator").
			Debugf("Missing name for creating dir: %#v", target)
		return ErrInternalServerError
	}
	rev, ok := target["_rev"].(string)
	if !ok {
		inst.Logger().WithField("nspace", "replicator").
			Debugf("Missing _rev for creating dir: %#v", target)
		return ErrInternalServerError
	}
	revisions, ok := target["_revisions"].(map[string]interface{})
	if !ok {
		inst.Logger().WithField("nspace", "replicator").
			Debugf("Missing _revisions for creating dir: %#v", target)
		return ErrInternalServerError
	}
	indexer := newSharingIndexer(inst, &bulkRevs{
		Rev:       rev,
		Revisions: revisions,
	})
	fs := inst.VFS().UseSharingIndexer(indexer)

	var parent *vfs.DirDoc
	var err error
	if dirID, ok := target["dir_id"].(string); ok {
		parent, err = fs.DirByID(dirID)
		if err == os.ErrNotExist {
			parent, err = s.recreateParent(inst, dirID)
		}
		if err != nil {
			inst.Logger().WithField("nspace", "replicator").
				Debugf("Conflict for parent on creating dir: %s", err)
			return err
		}
	} else {
		parent, err = s.GetSharingDir(inst)
		if err != nil {
			return err
		}
	}

	dir, err := vfs.NewDirDocWithParent(name, parent, nil)
	if err != nil {
		inst.Logger().WithField("nspace", "replicator").
			Warnf("Cannot initialize dir doc: %s", err)
		return err
	}
	dir.SetID(target["_id"].(string))
	copySafeFieldsToDir(target, dir)
	err = fs.CreateDir(dir)
	if err == os.ErrExist {
		name, errr := resolveConflictSamePath(inst, dir.DocID, dir.Fullpath)
		if errr != nil {
			return errr
		}
		if name != "" {
			indexer.IncrementRevision()
			dir.DocName = name
		}
		err = fs.CreateDir(dir)
	}
	if err != nil {
		inst.Logger().WithField("nspace", "replicator").
			Debugf("Cannot create dir: %s", err)
		return err
	}
	return nil
}

// UpdateDir updates a directory on this cozy to reflect a change on another
// cozy instance of this sharing.
func (s *Sharing) UpdateDir(inst *instance.Instance, target map[string]interface{}, dir *vfs.DirDoc) error {
	rev, ok := target["_rev"].(string)
	if !ok {
		inst.Logger().WithField("nspace", "replicator").
			Warnf("Missing _rev for updating directory %#v", target)
		return ErrInternalServerError
	}
	revisions, ok := target["_revisions"].(map[string]interface{})
	if !ok {
		inst.Logger().WithField("nspace", "replicator").
			Warnf("Missing _revisions for updating directory %#v", target)
		return ErrInternalServerError
	}
	oldDoc := dir.Clone().(*vfs.DirDoc)
	indexer := newSharingIndexer(inst, &bulkRevs{
		Rev:       rev,
		Revisions: revisions,
	})
	fs := inst.VFS().UseSharingIndexer(indexer)

	name, ok := target["name"].(string)
	if !ok {
		inst.Logger().WithField("nspace", "replicator").
			Warnf("Missing name for updating directory %#v", target)
		return ErrInternalServerError
	}
	dir.DocName = name
	if dirID, ok := target["dir_id"].(string); ok {
		if dirID != dir.DirID {
			parent, err := fs.DirByID(dirID)
			if err == os.ErrNotExist {
				parent, err = s.recreateParent(inst, dirID)
			}
			if err != nil {
				inst.Logger().WithField("nspace", "replicator").
					Debugf("Conflict for parent on updating dir: %s", err)
				return err
			}
			dir.DirID = parent.DocID
			dir.Fullpath = path.Join(parent.Fullpath, dir.DocName)
		} else {
			dir.Fullpath = path.Join(path.Dir(dir.Fullpath), dir.DocName)
		}
	} else {
		parent, err := s.GetSharingDir(inst)
		if err != nil {
			return err
		}
		dir.DirID = parent.DocID
		dir.Fullpath = path.Join(parent.Fullpath, dir.DocName)
	}

	copySafeFieldsToDir(target, dir)
	// TODO detect & resolve conflicts when both instances have updated the dir concurrently
	err := fs.UpdateDirDoc(oldDoc, dir)
	if err == os.ErrExist {
		name, errr := resolveConflictSamePath(inst, dir.DocID, dir.Fullpath)
		if errr != nil {
			return errr
		}
		if name != "" {
			indexer.IncrementRevision()
			dir.DocName = name
		}
		err = fs.UpdateDirDoc(oldDoc, dir)
	}
	if err != nil {
		inst.Logger().WithField("nspace", "replicator").
			Debugf("Cannot update dir: %s", err)
		return err
	}
	return nil
}

// TrashDir puts the directory in the trash (except if the directory has a
// reference, in which case, we keep it in a special folder)
func (s *Sharing) TrashDir(inst *instance.Instance, dir *vfs.DirDoc) error {
	if strings.HasPrefix(dir.Fullpath+"/", vfs.TrashDirName+"/") {
		// nothing to do if the directory is already in the trash
		return nil
	}
	if len(dir.ReferencedBy) == 0 {
		_, err := vfs.TrashDir(inst.VFS(), dir)
		return err
	}
	olddoc := dir.Clone().(*vfs.DirDoc)
	parent, err := s.GetNoLongerSharedDir(inst)
	if err != nil {
		return err
	}
	dir.DirID = parent.DocID
	dir.Fullpath = path.Join(parent.Fullpath, dir.DocName)
	return inst.VFS().UpdateDirDoc(olddoc, dir)
}

// TrashFile puts the file in the trash (except if the file has a reference, in
// which case, we keep it in a special folder)
func (s *Sharing) TrashFile(inst *instance.Instance, file *vfs.FileDoc, rule *Rule) error {
	if file.Trashed {
		// nothing to do if the directory is already in the trash
		return nil
	}
	olddoc := file.Clone().(*vfs.FileDoc)
	removeReferencesFromRule(file, rule)
	if len(file.ReferencedBy) == 0 {
		_, err := vfs.TrashFile(inst.VFS(), file)
		return err
	}
	parent, err := s.GetNoLongerSharedDir(inst)
	if err != nil {
		return err
	}
	file.DirID = parent.DocID
	return inst.VFS().UpdateFileDoc(olddoc, file)
}

func dirToJSONDoc(dir *vfs.DirDoc) couchdb.JSONDoc {
	doc := couchdb.JSONDoc{
		Type: consts.Files,
		M: map[string]interface{}{
			"type":       dir.Type,
			"_id":        dir.DocID,
			"_rev":       dir.DocRev,
			"name":       dir.DocName,
			"created_at": dir.CreatedAt,
			"updated_at": dir.UpdatedAt,
			"tags":       dir.Tags,
			"path":       dir.Fullpath,
			couchdb.SelectorReferencedBy: dir.ReferencedBy,
		},
	}
	if dir.DirID != "" {
		doc.M["dir_id"] = dir.DirID
	}
	if dir.RestorePath != "" {
		doc.M["restore_path"] = dir.RestorePath
	}
	return doc
}

func fileToJSONDoc(file *vfs.FileDoc) couchdb.JSONDoc {
	doc := couchdb.JSONDoc{
		Type: consts.Files,
		M: map[string]interface{}{
			"type":       file.Type,
			"_id":        file.DocID,
			"_rev":       file.DocRev,
			"name":       file.DocName,
			"created_at": file.CreatedAt,
			"updated_at": file.UpdatedAt,
			"size":       file.ByteSize,
			"md5Sum":     file.MD5Sum,
			"mime":       file.Mime,
			"class":      file.Class,
			"executable": file.Executable,
			"trashed":    file.Trashed,
			"tags":       file.Tags,
			couchdb.SelectorReferencedBy: file.ReferencedBy,
		},
	}
	if file.DirID != "" {
		doc.M["dir_id"] = file.DirID
	}
	if file.RestorePath != "" {
		doc.M["restore_path"] = file.RestorePath
	}
	if len(file.Metadata) > 0 {
		doc.M["metadata"] = file.Metadata
	}
	return doc
}
