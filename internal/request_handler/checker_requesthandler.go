package requesthandler

import (
	"net/http"

	"github.com/christianfischer/md-wiki-server/internal/checker"
	"github.com/go-fuego/fuego"
)

type checkerRequest struct {
	Text     string `json:"text" validate:"required"`
	Language string `json:"language,omitempty"`
}

type checkerResponse struct {
	Annotations []checker.Annotation `json:"annotations"`
	Language    string               `json:"language,omitempty"`
}

type CheckerRequesthandler struct {
	checker checker.Checker
}

func NewCheckerRequesthandler(c checker.Checker) *CheckerRequesthandler {
	return &CheckerRequesthandler{checker: c}
}

func (h *CheckerRequesthandler) Check(c fuego.ContextWithBody[checkerRequest]) (checkerResponse, error) {
	if h.checker == nil {
		return checkerResponse{}, fuego.HTTPError{Status: http.StatusServiceUnavailable, Detail: "Checker not configured"}
	}

	body, err := c.Body()
	if err != nil {
		return checkerResponse{}, fuego.HTTPError{Status: http.StatusBadRequest, Detail: "Invalid request body"}
	}

	result, err := h.checker.Check(c.Context(), body.Text, body.Language)
	if err != nil {
		return checkerResponse{}, fuego.HTTPError{Status: http.StatusBadGateway, Detail: "Checker error: " + err.Error()}
	}

	annotations := result.Annotations
	if annotations == nil {
		annotations = []checker.Annotation{}
	}

	return checkerResponse{
		Annotations: annotations,
		Language:    result.Language,
	}, nil
}
