package sharing

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/cozy/cozy-stack/client/request"
	"github.com/cozy/cozy-stack/pkg/consts"
	"github.com/cozy/cozy-stack/pkg/couchdb"
	"github.com/cozy/cozy-stack/pkg/instance"
	"github.com/cozy/cozy-stack/pkg/lock"
	"github.com/cozy/cozy-stack/pkg/vfs"
	multierror "github.com/hashicorp/go-multierror"
)

// UploadMsg is used for jobs on the share-upload worker.
type UploadMsg struct {
	SharingID string `json:"sharing_id"`
	Errors    int    `json:"errors"`
}

// Upload starts uploading files for this sharing
func (s *Sharing) Upload(inst *instance.Instance, errors int) error {
	mu := lock.ReadWrite(inst.Domain + "/sharings/" + s.SID + "/upload")
	mu.Lock()
	defer mu.Unlock()

	var errm error
	var members []*Member
	if !s.Owner {
		members = append(members, &s.Members[0])
	} else {
		for i, m := range s.Members {
			if i == 0 {
				continue
			}
			if m.Status == MemberStatusReady {
				members = append(members, &s.Members[i])
			}
		}
	}

	// TODO what if we have more than BatchSize files to upload?
	for i := 0; i < BatchSize; i++ {
		if len(members) == 0 {
			break
		}
		m := members[0]
		members = members[1:]
		more, err := s.UploadTo(inst, m)
		if err != nil {
			errm = multierror.Append(errm, err)
		}
		if more {
			members = append(members, m)
		}
	}

	if errm != nil {
		s.retryWorker(inst, "share-upload", errors)
		fmt.Printf("DEBUG errm=%s\n", errm)
	}
	return errm
}

// InitialUpload uploads files to just a member, for the first time
func (s *Sharing) InitialUpload(inst *instance.Instance, m *Member) error {
	mu := lock.ReadWrite(inst.Domain + "/sharings/" + s.SID + "/upload")
	mu.Lock()
	defer mu.Unlock()

	// TODO what if we have more than BatchSize files to upload?
	for i := 0; i < BatchSize; i++ {
		more, err := s.UploadTo(inst, m)
		if err != nil {
			return err
		}
		if !more {
			return nil
		}
	}

	return nil
}

// UploadTo uploads one file to the given member. It returns false if there
// are no more files to upload to this member currently.
func (s *Sharing) UploadTo(inst *instance.Instance, m *Member) (bool, error) {
	if m.Instance == "" {
		return false, ErrInvalidURL
	}
	creds := s.FindCredentials(m)
	if creds == nil {
		return false, ErrInvalidSharing
	}

	lastSeq, err := s.getLastSeqNumber(inst, m, "upload")
	if err != nil {
		return false, err
	}
	inst.Logger().WithField("nspace", "upload").Debugf("lastSeq = %s", lastSeq)

	file, ruleIndex, seq, err := s.findNextFileToUpload(inst, lastSeq)
	if err != nil {
		return false, err
	}
	if file == nil {
		if seq != lastSeq {
			err = s.UpdateLastSequenceNumber(inst, m, "upload", seq)
		}
		return false, err
	}

	if err = s.uploadFile(inst, m, file, ruleIndex); err != nil {
		return false, err
	}

	return true, s.UpdateLastSequenceNumber(inst, m, "upload", seq)
}

// findNextFileToUpload uses the changes feed to find the next file that needs
// to be uploaded. It returns a file document if there is one file to upload,
// and the sequence number where it is in the changes feed.
func (s *Sharing) findNextFileToUpload(inst *instance.Instance, since string) (map[string]interface{}, int, string, error) {
	for {
		response, err := couchdb.GetChanges(inst, &couchdb.ChangesRequest{
			DocType:     consts.Shared,
			IncludeDocs: true,
			Since:       since,
			Limit:       1,
		})
		if err != nil {
			return nil, 0, since, err
		}
		since = response.LastSeq
		if len(response.Results) == 0 {
			break
		}
		r := response.Results[0]
		infos, ok := r.Doc.Get("infos").(map[string]interface{})
		if !ok {
			continue
		}
		info, ok := infos[s.SID].(map[string]interface{})
		if !ok {
			continue
		}
		if _, ok = info["binary"]; !ok {
			continue
		}
		if _, ok = info["removed"]; ok {
			continue
		}
		idx, ok := info["rule"].(float64)
		if !ok {
			continue
		}
		rev := extractLastRevision(r.Doc)
		if rev == "" {
			continue
		}
		docID := strings.SplitN(r.DocID, "/", 2)[1]
		ir := couchdb.IDRev{ID: docID, Rev: rev}
		query := []couchdb.IDRev{ir}
		results, err := couchdb.BulkGetDocs(inst, consts.Files, query)
		if err != nil {
			return nil, 0, since, err
		}
		if len(results) == 0 {
			return nil, 0, since, ErrInternalServerError
		}
		return results[0], int(idx), since, nil
	}
	return nil, 0, since, nil
}

// uploadFile uploads one file to the given member. It first try to just send
// the metadata, and if it is not enough, it also send the binary.
func (s *Sharing) uploadFile(inst *instance.Instance, m *Member, file map[string]interface{}, ruleIndex int) error {
	creds := s.FindCredentials(m)
	if creds == nil {
		return ErrInvalidSharing
	}
	u, err := url.Parse(m.Instance)
	if err != nil {
		return err
	}
	origFileID := file["_id"].(string)
	s.TransformFileToSent(file, creds.XorKey, ruleIndex)
	xoredFileID := file["_id"].(string)
	body, err := json.Marshal(file)
	if err != nil {
		return err
	}
	opts := &request.Options{
		Method: http.MethodPut,
		Scheme: u.Scheme,
		Domain: u.Host,
		Path:   "/sharings/" + s.SID + "/io.cozy.files/" + xoredFileID + "/metadata",
		Headers: request.Headers{
			"Accept":        "application/json",
			"Content-Type":  "application/json",
			"Authorization": "Bearer " + creds.AccessToken.AccessToken,
		},
		Body: bytes.NewReader(body),
	}
	var res *http.Response
	res, err = request.Req(opts)
	if err != nil {
		return err
	}
	if res.StatusCode/100 == 4 {
		res.Body.Close()
		res, err = RefreshToken(inst, s, m, creds, opts, body)
		if err != nil {
			return err
		}
	}
	defer res.Body.Close()
	if res.StatusCode/100 == 5 {
		return ErrInternalServerError
	}

	if res.StatusCode == 204 {
		return nil
	}
	var resBody KeyToUpload
	if err = json.NewDecoder(res.Body).Decode(&resBody); err != nil {
		return err
	}

	fs := inst.VFS()
	fileDoc, err := fs.FileByID(origFileID)
	if err != nil {
		return err
	}
	content, err := fs.OpenFile(fileDoc)
	if err != nil {
		return err
	}
	defer content.Close()

	res2, err := request.Req(&request.Options{
		Method: http.MethodPut,
		Scheme: u.Scheme,
		Domain: u.Host,
		Path:   "/sharings/" + s.SID + "/io.cozy.files/" + resBody.Key,
		Headers: request.Headers{
			"Authorization": "Bearer " + creds.AccessToken.AccessToken,
			"Content-Type":  fileDoc.Mime,
		},
		Body: content,
	})
	if err != nil {
		return err
	}
	res2.Body.Close()
	if res2.StatusCode/100 == 5 {
		return ErrInternalServerError
	}
	if res2.StatusCode/100 != 2 {
		return ErrClientError
	}
	return nil
}

// FileDocWithRevisions is the struct of the payload for synchronizing a file
type FileDocWithRevisions struct {
	*vfs.FileDoc
	Revisions map[string]interface{} `json:"_revisions"`
}

// KeyToUpload contains the key for uploading a file (when syncing metadata is
// not enough)
type KeyToUpload struct {
	Key string `json:"key"`
}

func (s *Sharing) createUploadKey(inst *instance.Instance, target *FileDocWithRevisions) (*KeyToUpload, error) {
	key, err := getStore().Save(inst.Domain, target)
	if err != nil {
		return nil, err
	}
	return &KeyToUpload{Key: key}, nil
}

// SyncFile tries to synchronize a file with just the metadata. If it can't,
// it will return a key to upload the content.
func (s *Sharing) SyncFile(inst *instance.Instance, target *FileDocWithRevisions) (*KeyToUpload, error) {
	if len(target.MD5Sum) == 0 {
		return nil, vfs.ErrInvalidHash
	}
	current, err := inst.VFS().FileByID(target.DocID)
	if err != nil {
		if err == os.ErrNotExist {
			// TODO check that there is a rule that allow to add this file
			return s.createUploadKey(inst, target)
		}
		return nil, err
	}
	var ref SharedRef
	err = couchdb.GetDoc(inst, consts.Shared, consts.Files+"/"+target.DocID, &ref)
	if err != nil {
		if couchdb.IsNotFoundError(err) {
			return nil, ErrSafety
		}
		return nil, err
	}
	infos, ok := ref.Infos[s.SID]
	if !ok {
		return nil, ErrSafety
	}
	rule := &s.Rules[infos.Rule]
	if RevGeneration(current.DocRev) >= RevGeneration(target.DocRev) {
		// TODO conflicts
		return nil, nil
	}
	if !bytes.Equal(target.MD5Sum, current.MD5Sum) {
		return s.createUploadKey(inst, target)
	}
	return nil, s.updateFileMetadata(inst, target, current, rule)
}

func (s *Sharing) updateFileMetadata(inst *instance.Instance, target *FileDocWithRevisions, current *vfs.FileDoc, rule *Rule) error {
	indexer := newSharingIndexer(inst, &bulkRevs{
		Rev:       target.Rev(),
		Revisions: target.Revisions,
	})
	fs := inst.VFS().UseSharingIndexer(indexer)
	oldDoc := current.Clone().(*vfs.FileDoc)

	var parent *vfs.DirDoc
	var err error
	if target.DirID == "" {
		parent, err = s.GetSharingDir(inst)
		if err != nil {
			return err
		}
		current.DirID = parent.DocID
	} else if target.DirID != current.DirID {
		parent, err = fs.DirByID(target.DirID)
		if err == os.ErrNotExist {
			parent, err = s.recreateParent(inst, target.DirID)
		}
		if err != nil {
			inst.Logger().WithField("nspace", "upload").
				Debugf("Conflict for parent on sync file: %s", err)
			return err
		}
	}
	current.DirID = parent.DocID
	current.DocName = target.DocName

	copySafeFieldsToFile(target.FileDoc, current)
	current.ReferencedBy = buildReferencedBy(target.FileDoc, current, rule)
	err = fs.UpdateFileDoc(oldDoc, current)
	if err == os.ErrExist {
		pth, errp := current.Path(fs)
		if errp != nil {
			return errp
		}
		name, errr := resolveConflictSamePath(inst, current.DocID, pth)
		if errr != nil {
			return errr
		}
		if name != "" {
			indexer.IncrementRevision()
			current.DocName = name
		}
		err = fs.UpdateFileDoc(oldDoc, current)
	}
	if err != nil {
		inst.Logger().WithField("nspace", "upload").
			Debugf("Cannot update file: %s", err)
		return err
	}
	return nil
}

// HandleFileUpload is used to receive a file upload when synchronizing just
// the metadata was not enough.
//
// Note: if file was renamed + its content has changed, we modify the content
// first, then rename it, not trying to do both at the same time. We do it in
// this order because the difficult case is if one operation succeeds and the
// other fails (if the two suceeds, we are fine; if the two fails, we just
// retry), and in that case, it is easier to manage a conflict on dir_id+name
// than on content: a conflict on different content is resolved by a copy of
// the file (which is not what we want), a conflict of name+dir_id, the higher
// revision wins and it should be the good one in our case.
func (s *Sharing) HandleFileUpload(inst *instance.Instance, key string, body io.ReadCloser) error {
	target, err := getStore().Get(inst.Domain, key)
	inst.Logger().WithField("nspace", "upload").
		Debugf("target = %#v\n", target)
	if err != nil {
		return err
	}
	if target == nil {
		return ErrMissingFileMetadata
	}

	indexer := newSharingIndexer(inst, &bulkRevs{
		Rev:       target.Rev(),
		Revisions: target.Revisions,
	})
	fs := inst.VFS().UseSharingIndexer(indexer)

	if target.DirID != "" {
		_, err = fs.DirByID(target.DirID)
		// TODO better handling of this conflict
		if err != nil {
			inst.Logger().WithField("nspace", "upload").
				Debugf("Conflict for parent on file upload: %s", err)
			return err
		}
	} else {
		parent, errb := s.GetSharingDir(inst)
		if errb != nil {
			return errb
		}
		target.DirID = parent.DocID
	}

	// TODO manage conflicts
	newdoc := target.FileDoc.Clone().(*vfs.FileDoc)
	olddoc, err := fs.FileByID(target.ID())
	if err != nil && err != os.ErrNotExist {
		return err
	}
	if olddoc == nil {
		rule := s.findRuleForNewFile(newdoc)
		if rule == nil {
			return ErrSafety
		}
		newdoc.ReferencedBy = buildReferencedBy(target.FileDoc, nil, rule)
		// TODO create the io.cozy.shared reference?
		return s.updateFileContent(inst, fs, newdoc, nil, body)
	}

	var ref SharedRef
	err = couchdb.GetDoc(inst, consts.Shared, consts.Files+"/"+target.DocID, &ref)
	if err != nil {
		if couchdb.IsNotFoundError(err) {
			return ErrSafety
		}
		return err
	}
	infos, ok := ref.Infos[s.SID]
	if !ok {
		return ErrSafety
	}
	rule := &s.Rules[infos.Rule]
	newdoc.ReferencedBy = buildReferencedBy(target.FileDoc, olddoc, rule)

	if newdoc.DocName == olddoc.DocName && newdoc.DirID == olddoc.DirID {
		// TODO update the io.cozy.shared reference?
		return s.updateFileContent(inst, fs, newdoc, olddoc, body)
	}

	// TODO Can we use a revision generated by CouchDB for the first operation
	// (content modified), and not forcing a revision? If we can remove this
	// revision after the renaming, it should be fine. Else, there is a risk
	// that it can be seen as a conflict
	if err = s.updateFileContent(inst, inst.VFS(), newdoc, olddoc, body); err != nil {
		return err
	}
	// TODO update the io.cozy.shared reference?
	return fs.UpdateFileDoc(newdoc, target.FileDoc)
}

func (s *Sharing) updateFileContent(inst *instance.Instance, fs vfs.VFS, newdoc, olddoc *vfs.FileDoc, body io.ReadCloser) (err error) {
	if olddoc == nil {
		newdoc.DocRev = ""
	} else {
		newdoc.DocName = olddoc.DocName
		newdoc.DirID = olddoc.DirID
	}
	file, err := fs.CreateFile(newdoc, olddoc)
	if err == os.ErrExist {
		pth, errp := newdoc.Path(fs)
		if errp != nil {
			return errp
		}
		name, errr := resolveConflictSamePath(inst, newdoc.DocID, pth)
		if errr != nil {
			return errr
		}
		if name != "" {
			// TODO we should generate a new revision to let the other cozy know of this change
			if olddoc == nil {
				newdoc.DocName = name
			} else {
				newdoc.DocName = olddoc.DocName
				newdoc.DirID = olddoc.DirID
			}
		}
		file, err = fs.CreateFile(newdoc, olddoc)
	}
	if err != nil {
		inst.Logger().WithField("nspace", "upload").
			Debugf("Cannot create file: %s", err)
		return err
	}
	defer func() {
		if cerr := file.Close(); cerr != nil && err == nil {
			err = cerr
			inst.Logger().WithField("nspace", "upload").
				Debugf("Cannot close file descriptor: %s", err)
		}
	}()
	_, err = io.Copy(file, body)
	return err
}
