package requesthandler

import (
	"net/http"

	"github.com/christianfischer/md-wiki-server/internal/store/ai_conversation"
	"github.com/gin-gonic/gin"
	"github.com/go-fuego/fuego"
)

type conversationCreateRequest struct {
	Title    string `json:"title"`
	Scope    string `json:"scope"`
	RepoSlug string `json:"repo_slug"`
	FilePath string `json:"file_path"`
}

type conversationUpdateRequest struct {
	Title string `json:"title" validate:"required"`
}

type conversationListResponse struct {
	Conversations []ai_conversation.Conversation `json:"conversations"`
}

type conversationDetailResponse struct {
	ai_conversation.Conversation
	Messages []ai_conversation.Message `json:"messages"`
}

type ConversationRequesthandler struct {
	store *ai_conversation.Store
}

func NewConversationRequesthandler(store *ai_conversation.Store) *ConversationRequesthandler {
	return &ConversationRequesthandler{store: store}
}

func (h *ConversationRequesthandler) List(c fuego.ContextNoBody) (conversationListResponse, error) {
	gc := c.Context().(*gin.Context)
	identity, _ := gc.Get("user_identity")
	userEmail, _ := identity.(string)
	if userEmail == "" {
		return conversationListResponse{}, fuego.HTTPError{Status: http.StatusUnauthorized, Detail: "authentication required"}
	}

	convs, err := h.store.ListConversations(gc.Request.Context(), userEmail, 50, 0)
	if err != nil {
		return conversationListResponse{}, fuego.HTTPError{Status: http.StatusInternalServerError, Detail: "failed to list conversations"}
	}
	if convs == nil {
		convs = []ai_conversation.Conversation{}
	}
	return conversationListResponse{Conversations: convs}, nil
}

func (h *ConversationRequesthandler) Create(c fuego.ContextWithBody[conversationCreateRequest]) (ai_conversation.Conversation, error) {
	gc := c.Context().(*gin.Context)
	identity, _ := gc.Get("user_identity")
	userEmail, _ := identity.(string)
	if userEmail == "" {
		return ai_conversation.Conversation{}, fuego.HTTPError{Status: http.StatusUnauthorized, Detail: "authentication required"}
	}

	body, err := c.Body()
	if err != nil {
		return ai_conversation.Conversation{}, err
	}

	scope := body.Scope
	if scope == "" {
		scope = "all"
	}

	conv, err := h.store.CreateConversation(gc.Request.Context(), ai_conversation.Conversation{
		UserEmail: userEmail,
		Title:     body.Title,
		Scope:     scope,
		RepoSlug:  body.RepoSlug,
		FilePath:  body.FilePath,
	})
	if err != nil {
		return ai_conversation.Conversation{}, fuego.HTTPError{Status: http.StatusInternalServerError, Detail: "failed to create conversation"}
	}
	return *conv, nil
}

func (h *ConversationRequesthandler) Get(c fuego.ContextNoBody) (conversationDetailResponse, error) {
	gc := c.Context().(*gin.Context)
	identity, _ := gc.Get("user_identity")
	userEmail, _ := identity.(string)
	if userEmail == "" {
		return conversationDetailResponse{}, fuego.HTTPError{Status: http.StatusUnauthorized, Detail: "authentication required"}
	}

	id := gc.Param("id")
	conv, err := h.store.GetConversation(gc.Request.Context(), id, userEmail)
	if err != nil {
		return conversationDetailResponse{}, fuego.HTTPError{Status: http.StatusInternalServerError, Detail: "failed to get conversation"}
	}
	if conv == nil {
		return conversationDetailResponse{}, fuego.HTTPError{Status: http.StatusNotFound, Detail: "conversation not found"}
	}

	msgs, err := h.store.GetMessages(gc.Request.Context(), id, userEmail)
	if err != nil {
		return conversationDetailResponse{}, fuego.HTTPError{Status: http.StatusInternalServerError, Detail: "failed to get messages"}
	}
	if msgs == nil {
		msgs = []ai_conversation.Message{}
	}

	return conversationDetailResponse{Conversation: *conv, Messages: msgs}, nil
}

func (h *ConversationRequesthandler) Update(c fuego.ContextWithBody[conversationUpdateRequest]) (ai_conversation.Conversation, error) {
	gc := c.Context().(*gin.Context)
	identity, _ := gc.Get("user_identity")
	userEmail, _ := identity.(string)
	if userEmail == "" {
		return ai_conversation.Conversation{}, fuego.HTTPError{Status: http.StatusUnauthorized, Detail: "authentication required"}
	}

	id := gc.Param("id")
	body, err := c.Body()
	if err != nil {
		return ai_conversation.Conversation{}, err
	}

	if err := h.store.UpdateTitle(gc.Request.Context(), id, userEmail, body.Title); err != nil {
		return ai_conversation.Conversation{}, fuego.HTTPError{Status: http.StatusInternalServerError, Detail: "failed to update conversation"}
	}

	conv, err := h.store.GetConversation(gc.Request.Context(), id, userEmail)
	if err != nil || conv == nil {
		return ai_conversation.Conversation{}, fuego.HTTPError{Status: http.StatusNotFound, Detail: "conversation not found"}
	}
	return *conv, nil
}

func (h *ConversationRequesthandler) Delete(c fuego.ContextNoBody) (struct{}, error) {
	gc := c.Context().(*gin.Context)
	identity, _ := gc.Get("user_identity")
	userEmail, _ := identity.(string)
	if userEmail == "" {
		return struct{}{}, fuego.HTTPError{Status: http.StatusUnauthorized, Detail: "authentication required"}
	}

	id := gc.Param("id")
	if err := h.store.DeleteConversation(gc.Request.Context(), id, userEmail); err != nil {
		return struct{}{}, fuego.HTTPError{Status: http.StatusInternalServerError, Detail: "failed to delete conversation"}
	}
	return struct{}{}, nil
}
