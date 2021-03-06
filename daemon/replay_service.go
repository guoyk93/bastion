package daemon

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/olivere/elastic"

	"github.com/guoyk93/bastion/daemon/models"

	"github.com/guoyk93/bastion/types"
	"github.com/guoyk93/bastion/utils"
	"golang.org/x/net/context"
)

func (d *Daemon) WriteReplay(s types.ReplayService_WriteReplayServer) (err error) {
	var w *os.File
	var zw *gzip.Writer
	var sessionID int64
	for {
		var f *types.ReplayFrame
		// receive frame
		if f, err = s.Recv(); err != nil {
			if err == io.EOF {
				err = s.SendAndClose(&types.WriteReplayResponse{})
			}
			break
		}
		// ensure rec frame writer
		if zw == nil {
			// create filename
			sessionID = f.SessionId
			filename := FilenameForSessionID(sessionID, d.opts.ReplayDir)
			// ensure directory
			if err = os.MkdirAll(filepath.Dir(filename), 0750); err != nil {
				break
			}
			// open file
			if w, err = os.OpenFile(filename, os.O_RDWR|os.O_CREATE, 0640); err != nil {
				break
			}
			// create frame writer with GZIP
			zw = gzip.NewWriter(w)
		}
		// write the frame
		if err = utils.WriteReplayFrame(f, zw); err != nil {
			break
		}
	}
	// close GZIP writer
	if zw != nil {
		zw.Close()
	}
	// close the GZIP writer won't close the file, so we have to close it manually
	if w != nil {
		w.Close()
	}
	// submit replay to elasticsearch
	if sessionID > 0 {
		if err = d.submitReplay(sessionID); err != nil {
			return
		}
	}
	return
}

func (d *Daemon) ReadReplay(req *types.ReadReplayRequest, s types.ReplayService_ReadReplayServer) (err error) {
	filename := FilenameForSessionID(req.SessionId, d.opts.ReplayDir)
	var r *os.File
	if r, err = os.Open(filename); err != nil {
		return
	}
	defer r.Close()
	var zr *gzip.Reader
	if zr, err = gzip.NewReader(r); err != nil {
		return
	}
	defer zr.Close()
	for {
		var f types.ReplayFrame
		if err = utils.ReadReplayFrame(&f, zr); err != nil {
			if err == io.EOF {
				err = nil
			}
			break
		}
		f.SessionId = req.SessionId
		if err = s.Send(&f); err != nil {
			break
		}
	}
	return
}

func (d *Daemon) submitReplay(sessionID int64) (err error) {
	// find session
	s := models.Session{}
	if err = d.db.One("Id", sessionID, &s); err != nil {
		return
	}
	// open file
	filename := FilenameForSessionID(sessionID, d.opts.ReplayDir)
	var r *os.File
	if r, err = os.Open(filename); err != nil {
		return
	}
	defer r.Close()
	// unzip stream
	var zr *gzip.Reader
	if zr, err = gzip.NewReader(r); err != nil {
		return
	}
	defer zr.Close()
	// submitter
	st := NewReplaySubmitter(time.Unix(s.CreatedAt, 0), s.Id, s.Account, d.esClient)
	for {
		var f types.ReplayFrame
		if err = utils.ReadReplayFrame(&f, zr); err != nil {
			if err == io.EOF {
				err = nil
				break
			} else {
				return
			}
		}
		if err = st.Add(f); err != nil {
			return
		}
	}
	if err = st.Close(); err != nil {
		return
	}
	return
}

func (d *Daemon) SubmitReplay(ctx context.Context, req *types.SubmitReplayRequest) (resp *types.SubmitReplayResponse, err error) {
	if err = req.Validate(); err != nil {
		return
	}
	if err = d.submitReplay(req.SessionId); err != nil {
		return
	}
	resp = &types.SubmitReplayResponse{}
	return
}

func (d *Daemon) SearchReplay(ctx context.Context, req *types.SearchReplayRequest) (resp *types.SearchReplayResponse, err error) {
	if err = req.Validate(); err != nil {
		return
	}
	var sres *elastic.SearchResult
	if sres, err = d.esClient.Search().Index(types.ReplayElasticsearchIndexPrefix + "*").Query(elastic.NewTermQuery("content", req.Keyword)).From(0).Size(100).Do(context.Background()); err != nil {
		return
	}
	hits := sres.Hits
	if hits == nil {
		err = errRecordNotFound
		return
	}
	resp = &types.SearchReplayResponse{
		Results: []*types.ReplaySearchResult{},
	}
	for _, h := range hits.Hits {
		if h == nil {
			continue
		}
		if h.Source == nil {
			continue
		}
		var ri ReplayIndice
		if err = json.Unmarshal(*h.Source, &ri); err != nil {
			return
		}
		resp.Results = append(resp.Results, &types.ReplaySearchResult{
			SessionId: ri.SessionId,
			Timestamp: ri.Timestamp,
			Account:   ri.Account,
			CreatedAt: ri.CreatedAt.Unix(),
		})
	}
	return
}
