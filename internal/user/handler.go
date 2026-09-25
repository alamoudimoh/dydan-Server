package user

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/mail"
	"path/filepath"
	"strings"
	"time"

	"github.com/alamoudimoh/dydan-server/config"
	auth "github.com/alamoudimoh/dydan-server/internal/auth"
	"github.com/alamoudimoh/dydan-server/internal/auth/apple"
	"github.com/alamoudimoh/dydan-server/internal/authlimit"
	cModel "github.com/alamoudimoh/dydan-server/internal/circle/model"
	cRepo "github.com/alamoudimoh/dydan-server/internal/circle/repo"
	"github.com/alamoudimoh/dydan-server/internal/email"
	"github.com/alamoudimoh/dydan-server/internal/mfa"
	nModel "github.com/alamoudimoh/dydan-server/internal/notifier/model"
	"github.com/alamoudimoh/dydan-server/internal/outbound"
	storage "github.com/alamoudimoh/dydan-server/internal/storage"
	storageRepo "github.com/alamoudimoh/dydan-server/internal/storage/repo"
	uModel "github.com/alamoudimoh/dydan-server/internal/user/model"
	uRepo "github.com/alamoudimoh/dydan-server/internal/user/repo"
	"github.com/alamoudimoh/dydan-server/internal/utils"
	"github.com/alamoudimoh/dydan-server/logging"
	jwt "github.com/appleboy/gin-jwt/v2"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	limiter "github.com/ulule/limiter/v3"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/oauth2/v1"
	"google.golang.org/api/option"
	"gorm.io/gorm"
)

type Handler struct {
	userRepo               *uRepo.UserRepository
	circleRepo             *cRepo.CircleRepository
	jwtAuth                *jwt.GinJWTMiddleware
	tokenService           *auth.TokenService
	email                  email.Sender
	identityProvider       *auth.IdentityProvider
	isDydanCloud           bool
	IsUserCreationDisabled bool
	DydanCloudConfig       config.DydanCloudConfig
	storage                storage.Storage
	storageRepo            *storageRepo.StorageRepository
	signer                 storage.URLSigner
	deletionService        *DeletionService
	appleService           *apple.AppleService
	mfaService             *mfa.MFAService
	maxSubaccounts         int
	plusMaxSubaccounts     int
	oauth2Config           config.OAuth2Config
	oauthTransactions      *auth.OAuthTransactionStore
	singleCircleInstance   bool
	mfaMaxAttempts         int
	oauthRedirectURLs      []string
	serveSwagger           bool
	legal                  config.LegalConfig
	billingEnabled         bool
}

func NewHandler(ur *uRepo.UserRepository, cr *cRepo.CircleRepository,
	jwtAuth *jwt.GinJWTMiddleware, tokenService *auth.TokenService,
	email email.Sender,
	idp *auth.IdentityProvider, storage storage.Storage,
	signer storage.URLSigner, storageRepo *storageRepo.StorageRepository,
	appleService *apple.AppleService,
	deletionService *DeletionService, mfaService *mfa.MFAService, config *config.Config) *Handler {
	oauthRedirectURLs := append([]string{}, config.OAuth2Config.AllowedRedirectURLs...)
	if config.Server.PublicHost != "" {
		oauthRedirectURLs = append(oauthRedirectURLs, strings.TrimRight(config.Server.PublicHost, "/")+"/auth/oauth2")
	}
	return &Handler{
		userRepo:               ur,
		circleRepo:             cr,
		jwtAuth:                jwtAuth,
		tokenService:           tokenService,
		email:                  email,
		identityProvider:       idp,
		isDydanCloud:           config.IsDydanCloud,
		IsUserCreationDisabled: config.IsUserCreationDisabled,
		DydanCloudConfig:       config.DydanCloudConfig,
		storage:                storage,
		storageRepo:            storageRepo,
		signer:                 signer,
		deletionService:        deletionService,
		appleService:           appleService,
		mfaService:             mfaService,
		maxSubaccounts:         config.FeatureLimits.MaxSubaccounts,
		plusMaxSubaccounts:     config.FeatureLimits.PlusMaxSubaccounts,
		oauth2Config:           config.OAuth2Config,
		oauthTransactions:      auth.NewOAuthTransactionStore(),
		singleCircleInstance:   config.SingleCircleInstance,
		mfaMaxAttempts:         config.MFAConfig.MaxVerificationAttempts,
		oauthRedirectURLs:      oauthRedirectURLs,
		serveSwagger:           config.Server.ServeSwagger,
		legal:                  config.Legal,
		// Billing history is only offered when a payment provider is actually
		// configured; otherwise the Settings screen reports the capability as
		// absent instead of showing a view that could never return invoices.
		billingEnabled: strings.TrimSpace(config.StripeConfig.APIKey) != "",
	}
}

func (h *Handler) GetAllUsers() gin.HandlerFunc {
	return func(c *gin.Context) {
		currentUser, ok := auth.CurrentUser(c)
		if !ok {
			c.JSON(500, gin.H{
				"error": "Error getting current user",
			})
			return
		}

		users, err := h.userRepo.GetAllUsers(c, currentUser.CircleID)
		if err != nil {
			c.JSON(500, gin.H{
				"error": "Error getting users",
			})
			return
		}

		for i := range users {
			users[i].Image = h.signer.SignIfLocal(users[i].Image)
		}

		c.JSON(200, gin.H{
			"res": users,
		})
	}
}

func (h *Handler) signUp(c *gin.Context) {
	if h.IsUserCreationDisabled {
		c.JSON(403, gin.H{
			"error": "User creation is disabled",
		})
		return
	}

	type SignUpReq struct {
		Username    string `json:"username" binding:"required,min=4,max=20"`
		Password    string `json:"password" binding:"required,min=8,max=64"`
		Email       string `json:"email" binding:"required,email"`
		DisplayName string `json:"displayName"`
	}
	var signupReq SignUpReq
	if err := c.BindJSON(&signupReq); err != nil {
		c.JSON(400, gin.H{
			"error": "Invalid request",
		})
		return
	}
	if signupReq.DisplayName == "" {
		signupReq.DisplayName = signupReq.Username
	}

	// Validate username format
	if !utils.IsValidUsername(signupReq.Username) {
		c.JSON(400, gin.H{
			"error": "Username can only contain lowercase letters (a-z), numbers (0-9), dots (.), and hyphens (-)",
		})
		return
	}

	password, err := auth.EncodePassword(signupReq.Password)
	signupReq.Username = html.EscapeString(signupReq.Username)
	signupReq.DisplayName = html.EscapeString(signupReq.DisplayName)

	if err != nil {
		c.JSON(500, gin.H{
			"error": "Error encoding password",
		})
		return
	}
	var insertedUser *uModel.User
	if insertedUser, err = h.userRepo.CreateUser(c, &uModel.User{
		Username:    signupReq.Username,
		Password:    password,
		DisplayName: signupReq.DisplayName,
		Email:       signupReq.Email,
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}); err != nil {
		if errors.Is(err, uRepo.ErrUserAlreadyExists) {
			// Expected conflict, not an internal failure. The message mirrors
			// the information the previous error body already disclosed.
			c.JSON(http.StatusConflict, gin.H{
				"error": "Error creating user, email already exists or username is taken",
			})
			return
		}
		c.JSON(500, gin.H{
			"error": "Error creating user, email already exists or username is taken",
		})
		return
	}
	// var userCircle *circle.Circle
	// var userRole string
	userCircle, err := h.circleRepo.CreateCircle(c, &cModel.Circle{
		Name:       signupReq.DisplayName + "'s circle",
		CreatedAt:  time.Now().UTC(),
		UpdatedAt:  time.Now().UTC(),
		InviteCode: utils.GenerateInviteCode(c),
	})

	if err != nil {
		c.JSON(500, gin.H{
			"error": "Error creating circle",
		})
		return
	}

	if err := h.circleRepo.AddUserToCircle(c, &cModel.UserCircle{
		UserID:    insertedUser.ID,
		CircleID:  userCircle.ID,
		Role:      "admin",
		IsActive:  true,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		c.JSON(500, gin.H{
			"error": "Error adding user to circle",
		})
		return
	}
	insertedUser.CircleID = userCircle.ID
	if err := h.userRepo.UpdateUser(c, insertedUser); err != nil {
		c.JSON(500, gin.H{
			"error": "Error updating user",
		})
		return
	}
	if err := h.storageRepo.CreateStorageUsage(c, insertedUser.ID, userCircle.ID); err != nil {
		c.JSON(500, gin.H{
			"error": "Error initializing storage",
		})
		return
	}

	c.JSON(201, gin.H{})
}

func (h *Handler) GetUserProfile(c *gin.Context) {
	user, ok := auth.CurrentUser(c)
	if !ok {
		c.JSON(500, gin.H{
			"error": "Error getting user",
		})
		return
	}
	user.Image = h.signer.SignIfLocal(user.Image)
	c.JSON(200, gin.H{
		"res": user,
	})
}

func (h *Handler) syncOIDCRole(c *gin.Context, userID, circleID int, groups []string) {
	logger := logging.FromContext(c)
	resolvedRole, active := auth.ResolveRoleFromGroups(groups, h.oauth2Config.AdminGroups, h.oauth2Config.ManagerGroups)
	if !active {
		return
	}

	currentRole, err := h.circleRepo.GetUserCircleRole(c, circleID, userID)
	if err != nil {
		logger.Warnw("OIDC role sync: failed to get current role", "userID", userID, "circleID", circleID, "err", err)
		return
	}

	if currentRole == cModel.Role(resolvedRole) {
		return
	}

	// Managed (child) accounts never inherit elevated Circle capabilities
	// (see auth.ResolveEffectiveRole): OIDC group membership must not be able
	// to promote them to admin or manager, bypassing the guard enforced by
	// the interactive role-change endpoint.
	target, err := h.userRepo.GetUserByID(c, userID)
	if err != nil {
		logger.Warnw("OIDC role sync: failed to load target user", "userID", userID, "err", err)
		return
	}
	if target.UserType == uModel.UserTypeChild &&
		(resolvedRole == cModel.RoleAdmin || resolvedRole == cModel.RoleManager) {
		logger.Warnw("OIDC role sync: refused to elevate managed child account", "userID", userID, "circleID", circleID, "resolvedRole", resolvedRole)
		return
	}

	if err := h.circleRepo.ChangeUserRole(c, circleID, userID, resolvedRole); err != nil {
		logger.Errorw("OIDC role sync: failed to update role", "userID", userID, "circleID", circleID, "resolvedRole", resolvedRole, "err", err)
		return
	}
	logger.Infow("OIDC role sync", "userID", userID, "circleID", circleID, "oldRole", currentRole, "newRole", resolvedRole)
}

// nativeOAuthRedirectURIs are the fixed deep-link callbacks used by the
// mobile clients. They are constants of the client applications and are not
// attacker-controlled.
var nativeOAuthRedirectURIs = []string{
	"dydan://auth/oauth2",
}

// allowedOAuthRedirectURIs returns the strict allowlist of permitted OAuth2
// callback URLs for this deployment:
//   - the configured oauth2.redirect_url (operator-controlled)
//   - explicitly configured additional redirect URLs and the public host
//   - the fixed native deep-link callbacks
//
// Anything else supplied by a client is rejected, preventing open-redirect or
// authorization-code exfiltration via a browser-supplied redirect_uri.
func (h *Handler) allowedOAuthRedirectURIs() map[string]bool {
	allowed := make(map[string]bool)
	if h.oauth2Config.RedirectURL != "" {
		allowed[h.oauth2Config.RedirectURL] = true
	}
	for _, redirect := range h.oauthRedirectURLs {
		if redirect != "" {
			allowed[redirect] = true
		}
	}
	for _, uri := range nativeOAuthRedirectURIs {
		allowed[uri] = true
	}
	return allowed
}

// thirdPartyAuthStart bootstraps an OAuth2/OIDC login transaction:
//
//	POST /api/v1/auth/:provider/start  {"redirect_uri": "..."}   (optional body)
//
// It returns a cryptographically random single-use state bound to a PKCE S256
// verifier (kept server-side) plus the code_challenge to include in the
// authorize request.
func (h *Handler) thirdPartyAuthStart(c *gin.Context) {
	logger := logging.FromContext(c)
	provider := c.Param("provider")
	if provider != "oauth2" || !h.identityProvider.Enabled() {
		c.JSON(http.StatusNotFound, gin.H{"error": "Unknown sign-in provider"})
		return
	}

	var req struct {
		RedirectURI string `json:"redirect_uri"`
	}
	// Body is optional; an empty body just means "derive from request".
	_ = c.ShouldBindJSON(&req)

	redirect := req.RedirectURI
	if redirect == "" {
		if h.oauth2Config.RedirectURL != "" {
			redirect = h.oauth2Config.RedirectURL
		} else if len(h.oauthRedirectURLs) > 0 {
			redirect = h.oauthRedirectURLs[0]
		}
	}
	if redirect == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No usable redirect URI: configure oauth2.redirect_url"})
		return
	}
	allowed := h.allowedOAuthRedirectURIs()
	if !allowed[redirect] {
		logger.Warnw("oauth2 start rejected unapproved redirect_uri")
		c.JSON(http.StatusBadRequest, gin.H{"error": "Unapproved redirect URI"})
		return
	}

	txn, err := h.oauthTransactions.Start(redirect)
	if err != nil {
		logger.Errorw("oauth2 start failed to create transaction", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to start authentication"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"state":                 txn.State,
		"code_challenge":        txn.CodeChallenge,
		"code_challenge_method": "S256",
	})
}

// googleTokeninfoClient bounds the server-side verification call to Google's
// tokeninfo endpoint. The callback route is unauthenticated and attacker-
// supplied tokens trigger this outbound request; an unbounded client
// (http.DefaultClient) would let a stalled upstream connection pin handler
// goroutines. 10s matches the identity-provider budget in internal/auth.
func googleTokeninfoClient() *http.Client {
	return &http.Client{Timeout: googleTokeninfoTimeout}
}

const googleTokeninfoTimeout = 10 * time.Second

func (h *Handler) thirdPartyAuthCallback(c *gin.Context) {

	// read :provider from path param, if param is google check the token with google if it's valid and fetch the user details:
	logger := logging.FromContext(c)
	provider := c.Param("provider")
	logger.Infow("account.handler.thirdPartyAuthCallback", "provider", provider)

	switch provider {
	case "google":
		c.Set("auth_provider", "3rdPartyAuth")
		type OAuthRequest struct {
			Token    string `json:"token" binding:"required"`
			Provider string `json:"provider" binding:"required"`
		}
		var body OAuthRequest
		if err := c.ShouldBindJSON(&body); err != nil {
			logger.Errorw("account.handler.thirdPartyAuthCallback failed to bind", "err", err)
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "Invalid request: " + err.Error(),
			})
			return
		}

		// logger.Infow("account.handler.thirdPartyAuthCallback", "token", token)
		service, err := oauth2.NewService(c, option.WithHTTPClient(googleTokeninfoClient()))
		if err != nil {
			logger.Errorw("account.handler.thirdPartyAuthCallback failed to create oauth2 service", "err", err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "Authentication service unavailable",
			})
			return
		}

		tokenInfo, err := service.Tokeninfo().AccessToken(body.Token).Do()
		if err != nil {
			logger.Errorw("account.handler.thirdPartyAuthCallback failed to get token info", "err", err)
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "Invalid token",
			})
			return
		}
		if tokenInfo.Audience != h.DydanCloudConfig.GoogleClientID && tokenInfo.Audience != h.DydanCloudConfig.GoogleIOSClientID && tokenInfo.Audience != h.DydanCloudConfig.GoogleAndroidClientID {
			logger.Errorw("account.handler.thirdPartyAuthCallback token audience mismatch", "audience", tokenInfo.Audience)
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "Invalid token",
			})
			return
		}
		userinfo, err := service.Userinfo.Get().Do(googleapi.QueryParameter("access_token", body.Token))
		if err != nil {
			logger.Errorw("account.handler.thirdPartyAuthCallback failed to get token info", "err", err)
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "Invalid token",
			})
			return
		}

		acc, err := h.userRepo.FindByEmail(c, userinfo.Email)
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			logger.Errorw("account.handler.thirdPartyAuthCallback failed to resolve account", "err", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Unable to resolve user account"})
			return
		}

		if err != nil {
			// create a random password for the user using crypto/rand:
			password := auth.GenerateRandomPassword(12)
			encodedPassword, err := auth.EncodePassword(password) //nolint:ineffassign
			if err != nil {
				logger.Errorw("account.handler.thirdPartyAuthCallback failed to encode password", "err", err)
				c.JSON(http.StatusInternalServerError, gin.H{
					"error": "Unable to create user account",
				})
				return
			}
			account := &uModel.User{
				Username:    userinfo.Id,
				Email:       userinfo.Email,
				Image:       userinfo.Picture,
				Password:    encodedPassword,
				DisplayName: userinfo.GivenName,
				Provider:    uModel.AuthProviderGoogle,
			}
			createdUser, err := h.userRepo.CreateUser(c, account)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{
					"error": "Unable to create user",
				})
				return

			}
			// Create Circle for the user:
			userCircle, err := h.circleRepo.CreateCircle(c, &cModel.Circle{
				Name:       userinfo.GivenName + "'s circle",
				CreatedAt:  time.Now().UTC(),
				UpdatedAt:  time.Now().UTC(),
				InviteCode: utils.GenerateInviteCode(c),
			})

			if err != nil {
				c.JSON(500, gin.H{
					"error": "Error creating circle",
				})
				return
			}

			if err := h.circleRepo.AddUserToCircle(c, &cModel.UserCircle{
				UserID:    createdUser.ID,
				CircleID:  userCircle.ID,
				Role:      "admin",
				IsActive:  true,
				CreatedAt: time.Now().UTC(),
				UpdatedAt: time.Now().UTC(),
			}); err != nil {
				c.JSON(500, gin.H{
					"error": "Error adding user to circle",
				})
				return
			}
			createdUser.CircleID = userCircle.ID
			if err := h.userRepo.UpdateUser(c, createdUser); err != nil {
				c.JSON(500, gin.H{
					"error": "Error updating user",
				})
				return
			}
			if err := h.storageRepo.CreateStorageUsage(c, createdUser.ID, userCircle.ID); err != nil {
				c.JSON(500, gin.H{
					"error": "Error initializing storage",
				})
				return
			}
			// acc was never assigned in this branch: the FindByEmail error above
			// means no account existed, so point it at the freshly created user.
			acc = &uModel.UserDetails{User: *createdUser}
		}
		// Check if user has MFA enabled
		if acc.MFAEnabled {
			// Create MFA session for third-party auth
			sessionToken, err := h.mfaService.GenerateSessionToken()
			if err != nil {
				logger.Errorw("Failed to generate MFA session token", "error", err)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Authentication failed"})
				return
			}

			mfaSession := &uModel.MFASession{
				SessionToken: sessionToken,
				UserID:       acc.ID,
				AuthMethod:   "google",
				Verified:     false,
				CreatedAt:    time.Now().UTC(),
				ExpiresAt:    time.Now().UTC().Add(10 * time.Minute),
				UserData:     acc.Username,
			}

			if err := h.userRepo.CreateMFASession(c, mfaSession); err != nil {
				logger.Errorw("Failed to create MFA session", "error", err)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Authentication failed"})
				return
			}

			c.JSON(http.StatusOK, gin.H{
				"mfaRequired":  true,
				"sessionToken": sessionToken,
			})
			return
		}
		// Generate tokens including refresh token:
		tokenResponse, err := h.tokenService.GenerateTokens(c.Request.Context(), acc)
		if err != nil {
			logger.Errorw("Unable to generate tokens", "error", err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "Unable to Generate a Token",
			})
			return
		}
		auth.SetRefreshSessionCookie(c, tokenResponse.RefreshToken, h.tokenService.RefreshTokenExpiry())
		auth.WriteTokenResponse(c, http.StatusOK, tokenResponse)
		return
	case "apple":
		c.Set("auth_provider", "3rdPartyAuth")
		// AppleAuthRequest matches the structure of the incoming Apple auth payload
		type AppleAuthRequest struct {
			Provider string `json:"provider" binding:"required"`
			Data     struct {
				Provider string `json:"provider"`
				Result   struct {
					IDToken     string `json:"idToken"`
					AccessToken struct {
						Token string `json:"token"`
					} `json:"accessToken"`
					Profile struct {
						User       string `json:"user"`
						GivenName  string `json:"givenName"`
						FamilyName string `json:"familyName"`
						Email      string `json:"email"`
					} `json:"profile"`
				} `json:"result"`
			} `json:"data"`
		}

		var body AppleAuthRequest
		if err := c.ShouldBindJSON(&body); err != nil {
			logger.Errorw("account.handler.thirdPartyAuthCallback (apple) failed to bind", "err", err)
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "Invalid request: " + err.Error(),
			})
			return
		}

		// Validate the ID token - use the JWT from accessToken.token, not the short idToken
		idToken := body.Data.Result.IDToken
		if idToken == "" || len(idToken) < 100 { // JWT tokens are much longer
			// Fallback to accessToken.token which contains the actual JWT
			idToken = body.Data.Result.AccessToken.Token
		}
		userInfo, err := h.appleService.ValidateIDToken(c.Request.Context(), idToken)
		if err != nil {
			logger.Errorw("account.handler.thirdPartyAuthCallback (apple) failed to validate token", "err", err)
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "Invalid Apple ID token",
			})
			return
		}

		// Check if user exists
		acc, err := h.userRepo.FindByEmail(c, userInfo.Email)
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			logger.Errorw("account.handler.thirdPartyAuthCallback (apple) failed to resolve account", "err", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Unable to resolve user account"})
			return
		}

		if err != nil {
			// Create user account
			password := auth.GenerateRandomPassword(12)
			encodedPassword, err := auth.EncodePassword(password)
			if err != nil {
				logger.Errorw("account.handler.thirdPartyAuthCallback (apple) failed to encode password", "err", err)
				c.JSON(http.StatusInternalServerError, gin.H{
					"error": "Unable to create user account",
				})
				return
			}

			// Use provided names from profile or fallback to email
			displayName := body.Data.Result.Profile.GivenName
			if displayName == "" {
				displayName = userInfo.Email
			}

			account := &uModel.User{
				Username:    userInfo.Sub,
				Email:       userInfo.Email,
				Password:    encodedPassword,
				DisplayName: displayName,
				Provider:    uModel.AuthProviderApple,
			}

			createdUser, err := h.userRepo.CreateUser(c, account)
			if err != nil {
				logger.Errorw("account.handler.thirdPartyAuthCallback (apple) failed to create user", "err", err)
				c.JSON(http.StatusInternalServerError, gin.H{
					"error": "Unable to create user",
				})
				return
			}

			// Create Circle for the user
			userCircle, err := h.circleRepo.CreateCircle(c, &cModel.Circle{
				Name:       displayName + "'s circle",
				CreatedAt:  time.Now().UTC(),
				UpdatedAt:  time.Now().UTC(),
				InviteCode: utils.GenerateInviteCode(c),
			})

			if err != nil {
				logger.Errorw("account.handler.thirdPartyAuthCallback (apple) failed to create circle", "err", err)
				c.JSON(500, gin.H{
					"error": "Error creating circle",
				})
				return
			}

			if err := h.circleRepo.AddUserToCircle(c, &cModel.UserCircle{
				UserID:    createdUser.ID,
				CircleID:  userCircle.ID,
				Role:      "admin",
				IsActive:  true,
				CreatedAt: time.Now().UTC(),
				UpdatedAt: time.Now().UTC(),
			}); err != nil {
				logger.Errorw("account.handler.thirdPartyAuthCallback (apple) failed to add user to circle", "err", err)
				c.JSON(500, gin.H{
					"error": "Error adding user to circle",
				})
				return
			}

			createdUser.CircleID = userCircle.ID
			if err := h.userRepo.UpdateUser(c, createdUser); err != nil {
				logger.Errorw("account.handler.thirdPartyAuthCallback (apple) failed to update user", "err", err)
				c.JSON(500, gin.H{
					"error": "Error updating user",
				})
				return
			}
			if err := h.storageRepo.CreateStorageUsage(c, createdUser.ID, userCircle.ID); err != nil {
				logger.Errorw("account.handler.thirdPartyAuthCallback (apple) failed to create storage usage", "err", err)
				c.JSON(500, gin.H{
					"error": "Error initializing storage",
				})
				return
			}

			acc = &uModel.UserDetails{User: *createdUser}
		}

		// Check if user has MFA enabled
		if acc.MFAEnabled {
			// Create MFA session for Apple auth
			sessionToken, err := h.mfaService.GenerateSessionToken()
			if err != nil {
				logger.Errorw("Failed to generate MFA session token", "error", err)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Authentication failed"})
				return
			}

			mfaSession := &uModel.MFASession{
				SessionToken: sessionToken,
				UserID:       acc.ID,
				AuthMethod:   "apple",
				Verified:     false,
				CreatedAt:    time.Now().UTC(),
				ExpiresAt:    time.Now().UTC().Add(10 * time.Minute),
				UserData:     acc.Username,
			}

			if err := h.userRepo.CreateMFASession(c, mfaSession); err != nil {
				logger.Errorw("Failed to create MFA session", "error", err)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Authentication failed"})
				return
			}

			c.JSON(http.StatusOK, gin.H{
				"mfaRequired":  true,
				"sessionToken": sessionToken,
			})
			return
		}

		// Generate tokens including refresh token:
		tokenResponse, err := h.tokenService.GenerateTokens(c.Request.Context(), acc)
		if err != nil {
			logger.Errorw("Unable to generate tokens for Apple user", "error", err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "Unable to Generate a Token",
			})
			return
		}
		auth.SetRefreshSessionCookie(c, tokenResponse.RefreshToken, h.tokenService.RefreshTokenExpiry())
		auth.WriteTokenResponse(c, http.StatusOK, tokenResponse)
		return
	case "oauth2":
		c.Set("auth_provider", "3rdPartyAuth")
		// Read the ID token from the request bod
		type Request struct {
			Code        string `json:"code"`
			State       string `json:"state"`
			RedirectURI string `json:"redirect_uri"`
		}
		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			logger.Errorw("account.handler.thirdPartyAuthCallback (oauth2) failed to bind request", "err", err)
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request: " + err.Error()})
			return
		}

		// Validate that the code is not empty
		if req.Code == "" {
			logger.Errorw("account.handler.thirdPartyAuthCallback (oauth2) empty authorization code")
			c.JSON(http.StatusBadRequest, gin.H{"error": "Authorization code is required"})
			return
		}

		logger.Infow("account.handler.thirdPartyAuthCallback (oauth2) attempting to exchange code", "codeLength", len(req.Code), "redirectURI", req.RedirectURI)

		// The redirect URI must come from a strict server-side allowlist
		// (configured redirect_url + same-origin derived callback + native
		// deep links). A browser-supplied arbitrary value is never trusted.
		allowedRedirects := h.allowedOAuthRedirectURIs()
		exchangeRedirect := req.RedirectURI
		if exchangeRedirect == "" {
			exchangeRedirect = h.oauth2Config.RedirectURL
		}
		if exchangeRedirect == "" || !allowedRedirects[exchangeRedirect] {
			logger.Warnw("account.handler.thirdPartyAuthCallback (oauth2) rejected unapproved redirect_uri")
			c.JSON(http.StatusBadRequest, gin.H{"error": "Unapproved redirect URI"})
			return
		}

		// Server-enforced state: single use, expiring, bound to the redirect
		// URI and PKCE verifier issued at /oauth2/start.
		var codeVerifier string
		txn, txnErr := h.oauthTransactions.Consume(req.State)
		switch {
		case txnErr == nil:
			// Redeem exactly what was authorized at start time.
			if txn.RedirectURI != "" {
				exchangeRedirect = txn.RedirectURI
			}
			codeVerifier = txn.CodeVerifier
		case errors.Is(txnErr, auth.ErrExpiredOAuthState):
			logger.Warnw("account.handler.thirdPartyAuthCallback (oauth2) expired state")
			c.JSON(http.StatusBadRequest, gin.H{"error": "Sign-in session expired. Please try signing in again."})
			return
		case errors.Is(txnErr, auth.ErrUnknownOAuthState) && req.State != "":
			// Unknown state: forged/replayed, or produced by a client
			// predating the hardened flow. The legacy fallback is opt-in
			// because it weakens CSRF/replay protection.
			if !h.oauth2Config.AllowLegacyOAuthState {
				logger.Warnw("account.handler.thirdPartyAuthCallback (oauth2) unknown state (legacy fallback disabled)")
				c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid sign-in state. Please try signing in again."})
				return
			}
			logger.Warnw("account.handler.thirdPartyAuthCallback (oauth2) accepting legacy client without server state")
		default:
			if !h.oauth2Config.AllowLegacyOAuthState {
				logger.Warnw("account.handler.thirdPartyAuthCallback (oauth2) missing state")
				c.JSON(http.StatusBadRequest, gin.H{"error": "Missing sign-in state. Please try signing in again."})
				return
			}
			logger.Warnw("account.handler.thirdPartyAuthCallback (oauth2) accepting legacy client with missing state")
		}

		// Pass the redirect URI from the request if provided, otherwise use config default
		token, err := h.identityProvider.ExchangeToken(c, req.Code, exchangeRedirect, codeVerifier)

		if err != nil {
			logger.Errorw("account.handler.thirdPartyAuthCallback (oauth2) failed to exchange token", "err", err)
			// Return a more specific error message based on the OAuth2 error
			if strings.Contains(err.Error(), "invalid_grant") {
				c.JSON(http.StatusBadRequest, gin.H{
					"error": "Authorization code is invalid, expired, or already used. Please try the authentication process again.",
				})
			} else {
				c.JSON(http.StatusInternalServerError, gin.H{
					"error": "Failed to exchange authorization code for token",
				})
			}
			return
		}

		claims, err := h.identityProvider.GetUserInfo(c, token)
		if err != nil {
			logger.Errorw("account.handler.thirdPartyAuthCallback (oauth2) failed to get claims", "err", err)
			c.JSON(http.StatusBadGateway, gin.H{
				"error": "Failed to fetch user info from identity provider",
			})
			return
		}

		// Identity hardening: the IdP's email assertion is only trusted for
		// account linking when explicitly verified. An unverified external
		// address must never grant access to an existing Dydan account.
		if claims.EmailVerified != nil && !*claims.EmailVerified {
			logger.Warnw("account.handler.thirdPartyAuthCallback (oauth2) rejected unverified email claim")
			c.JSON(http.StatusUnauthorized, gin.H{
				"error": "Your email address is not verified with the identity provider. Verify it and try again.",
			})
			return
		}

		existingAccount, findErr := h.userRepo.FindByEmail(c, claims.Email)
		if findErr != nil && !errors.Is(findErr, gorm.ErrRecordNotFound) {
			logger.Errorw("account.handler.thirdPartyAuthCallback (oauth2) failed to resolve account", "err", findErr)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Unable to resolve user account"})
			return
		}
		if findErr == nil {
			// Linking an existing account by email requires a verified email
			// claim. Providers that do not expose email_verified are not
			// allowed to silently take over an existing password account.
			if claims.EmailVerified == nil || !*claims.EmailVerified {
				logger.Warnw("account.handler.thirdPartyAuthCallback (oauth2) refusing unverified link to existing account")
				c.JSON(http.StatusUnauthorized, gin.H{
					"error": "The identity provider did not confirm your email address. Contact your administrator to enable sign-in.",
				})
				return
			}
		}

		acc, err := existingAccount, findErr
		if err != nil {
			// Create user
			password := auth.GenerateRandomPassword(12)
			encodedPassword, err := auth.EncodePassword(password)
			if err != nil {
				logger.Error("account.handler.thirdPartyAuthCallback (oauth2) password encoding failed", "err", err)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Password encoding failed"})
				return
			}
			// Prefer the OIDC `preferred_username` claim for the username;
			// fall back to email so the unique column stays non-empty when
			// the provider doesn't supply one.
			username := claims.Username
			if username == "" {
				username = claims.Email
			}
			account := &uModel.User{
				Username:    username,
				Email:       claims.Email,
				Password:    encodedPassword,
				Image:       claims.Picture,
				DisplayName: claims.DisplayName,
				Provider:    uModel.AuthProviderOAuth2,
			}
			createdUser, err := h.userRepo.CreateUser(c, account)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{
					"error": "Unable to create user",
				})
				return

			}
			var circleID int
			if h.singleCircleInstance {
				// In single-circle mode, add user to the shared household circle (ID 1).
				// Create it if it doesn't exist yet (first user bootstraps the circle).
				sharedCircle, err := h.circleRepo.GetCircleByID(c, 1)
				if err != nil {
					sharedCircle, err = h.circleRepo.CreateCircle(c, &cModel.Circle{
						Name:       "Home",
						CreatedAt:  time.Now().UTC(),
						UpdatedAt:  time.Now().UTC(),
						InviteCode: utils.GenerateInviteCode(c),
					})
					if err != nil {
						c.JSON(500, gin.H{"error": "Error creating shared circle"})
						return
					}
				}
				circleID = sharedCircle.ID

				// Resolve role from OIDC groups; default to member if no match or no config.
				role := cModel.UserRole(cModel.RoleMember)
				if resolvedRole, active := auth.ResolveRoleFromGroups(claims.Groups, h.oauth2Config.AdminGroups, h.oauth2Config.ManagerGroups); active {
					role = cModel.UserRole(resolvedRole)
				}

				if err := h.circleRepo.AddUserToCircle(c, &cModel.UserCircle{
					UserID:    createdUser.ID,
					CircleID:  circleID,
					Role:      role,
					IsActive:  true,
					CreatedAt: time.Now().UTC(),
					UpdatedAt: time.Now().UTC(),
				}); err != nil {
					c.JSON(500, gin.H{"error": "Error adding user to circle"})
					return
				}
			} else {
				// Default: create a personal circle for the new user.
				userCircle, err := h.circleRepo.CreateCircle(c, &cModel.Circle{
					Name:       claims.DisplayName + "'s circle",
					CreatedAt:  time.Now().UTC(),
					UpdatedAt:  time.Now().UTC(),
					InviteCode: utils.GenerateInviteCode(c),
				})
				if err != nil {
					c.JSON(500, gin.H{"error": "Error creating circle"})
					return
				}
				circleID = userCircle.ID

				if err := h.circleRepo.AddUserToCircle(c, &cModel.UserCircle{
					UserID:    createdUser.ID,
					CircleID:  circleID,
					Role:      "admin",
					IsActive:  true,
					CreatedAt: time.Now().UTC(),
					UpdatedAt: time.Now().UTC(),
				}); err != nil {
					c.JSON(500, gin.H{"error": "Error adding user to circle"})
					return
				}

				// Sync role from OIDC groups if configured (may demote from admin).
				h.syncOIDCRole(c, createdUser.ID, circleID, claims.Groups)
			}

			createdUser.CircleID = circleID
			if err := h.userRepo.UpdateUser(c, createdUser); err != nil {
				c.JSON(500, gin.H{
					"error": "Error updating user",
				})
				return
			}
			if err := h.storageRepo.CreateStorageUsage(c, createdUser.ID, circleID); err != nil {
				c.JSON(500, gin.H{
					"error": "Error initializing storage",
				})
				return
			}
			acc = &uModel.UserDetails{User: *createdUser}
		}
		// Check if user has MFA enabled
		if acc.MFAEnabled {
			// Create MFA session for OAuth2 auth
			sessionToken, err := h.mfaService.GenerateSessionToken()
			if err != nil {
				logger.Error("Failed to generate MFA session token", "error", err)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Authentication failed"})
				return
			}

			mfaSession := &uModel.MFASession{
				SessionToken: sessionToken,
				UserID:       acc.ID,
				AuthMethod:   "oauth2",
				Verified:     false,
				CreatedAt:    time.Now().UTC(),
				ExpiresAt:    time.Now().UTC().Add(10 * time.Minute),
				UserData:     acc.Username,
			}

			if err := h.userRepo.CreateMFASession(c, mfaSession); err != nil {
				logger.Error("Failed to create MFA session", "error", err)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Authentication failed"})
				return
			}

			c.JSON(http.StatusOK, gin.H{
				"mfaRequired":  true,
				"sessionToken": sessionToken,
			})
			return
		}
		// Set profile image from OAuth provider if not already set
		if acc.Image == "" && claims.Picture != "" {
			err := h.userRepo.UpdateUserImage(c, acc.ID, claims.Picture)
			if err != nil {
				logger.Warn("Failed to update profile image", "userID", acc.ID, "err", err)
			}
		}
		// Sync role from OIDC groups on every login (IdP is source of truth).
		h.syncOIDCRole(c, acc.ID, acc.CircleID, claims.Groups)
		// Generate tokens including refresh token:
		tokenResponse, err := h.tokenService.GenerateTokens(c.Request.Context(), acc)
		if err != nil {
			logger.Errorw("Unable to generate tokens", "error", err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "Unable to Generate a Token",
			})
			return
		}
		auth.SetRefreshSessionCookie(c, tokenResponse.RefreshToken, h.tokenService.RefreshTokenExpiry())
		auth.WriteTokenResponse(c, http.StatusOK, tokenResponse)
		return
	}
}

func (h *Handler) resetPassword(c *gin.Context) {
	log := logging.FromContext(c)
	type ResetPasswordReq struct {
		Email string `json:"email" binding:"required,email"`
	}
	var req ResetPasswordReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Invalid request: " + err.Error(),
		})
		return
	}
	const responseMessage = "If the account can be recovered, password reset instructions will be sent."
	normalizedEmail := strings.ToLower(strings.TrimSpace(req.Email))
	c.JSON(http.StatusAccepted, gin.H{"message": responseMessage})
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		user, err := h.userRepo.FindByEmail(ctx, normalizedEmail)
		if err != nil || user.Provider != uModel.AuthProviderDydan {
			return
		}
		token, err := auth.GenerateEmailResetToken(ctx)
		if err != nil {
			log.Errorw("account.handler.resetPassword failed to generate credential", "err", err)
			return
		}
		if err := h.userRepo.SetPasswordResetToken(ctx, normalizedEmail, token); err != nil {
			log.Errorw("account.handler.resetPassword failed to persist credential", "err", err)
			return
		}
		if err := h.email.SendResetPasswordEmail(ctx, normalizedEmail, token); err != nil {
			log.Errorw("account.handler.resetPassword failed to send email", "err", err)
			if cleanupErr := h.userRepo.DeletePasswordResetToken(ctx, token); cleanupErr != nil {
				log.Errorw("account.handler.resetPassword failed to invalidate undelivered credential", "err", cleanupErr)
			}
		}
	}()
}

func (h *Handler) updateUserPassword(c *gin.Context) {
	logger := logging.FromContext(c)
	type RequestBody struct {
		Password string `json:"password" binding:"required,min=8,max=64"`
		Code     string `json:"code"`
	}
	var body RequestBody
	if err := c.ShouldBindJSON(&body); err != nil {
		logger.Errorw("user.handler.resetAccountPassword failed to bind", "err", err)
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Invalid request: " + err.Error(),
		})
		return

	}
	if body.Code == "" {
		body.Code = c.Query("c") // Legacy links created before fragment-based recovery.
	}
	emailAddress, code, err := email.DecodeEmailAndCode(body.Code)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid code"})
		return
	}
	password, err := auth.EncodePassword(body.Password)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Unable to process password",
		})
		return
	}

	err = h.userRepo.UpdatePasswordByToken(c.Request.Context(), emailAddress, code, password)
	if err != nil {
		if errors.Is(err, uRepo.ErrInvalidPasswordResetToken) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid or expired password reset link"})
			return
		}
		logger.Errorw("account.handler.resetAccountPassword failed", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Unable to reset password"})
		return
	}
	c.JSON(http.StatusOK, gin.H{})

}

func (h *Handler) UpdateUserDetails(c *gin.Context) {
	type UpdateUserReq struct {
		DisplayName *string `json:"displayName" binding:"omitempty"`
		ChatID      *int64  `json:"chatID" binding:"omitempty"`
		Image       *string `json:"image" binding:"omitempty"`
		Timezone    *string `json:"timezone" binding:"omitempty"`
	}
	user, ok := auth.CurrentUser(c)
	if !ok {
		c.JSON(500, gin.H{
			"error": "Error getting user",
		})
		return
	}
	var req UpdateUserReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{
			"error": "Invalid request: " + err.Error(),
		})
		return
	}
	// update non-nil fields:
	if req.DisplayName != nil {
		user.DisplayName = html.EscapeString(*req.DisplayName)
	}
	if req.ChatID != nil {
		user.ChatID = *req.ChatID
	}
	if req.Image != nil {
		user.Image = *req.Image
	}
	if req.Timezone != nil {
		if !utils.IsValidTimezone(*req.Timezone) {
			c.JSON(400, gin.H{
				"error": "Invalid timezone",
			})
			return
		}
		user.Timezone = *req.Timezone
	}

	if err := h.userRepo.UpdateUser(c, &user.User); err != nil {
		c.JSON(500, gin.H{
			"error": "Error updating user",
		})
		return
	}
	c.JSON(200, user)
}

// exportMyData returns a portable, user-scoped JSON document directly to the
// authenticated requester. It intentionally projects account fields instead of
// serializing the User model, so credentials and internal security state cannot
// enter a browser download.
func (h *Handler) exportMyData(c *gin.Context) {
	user, ok := auth.CurrentUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Authentication required"})
		return
	}
	preferences, err := h.userRepo.GetUserPreferences(c.Request.Context(), user.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Unable to prepare data export"})
		return
	}
	var preferenceData interface{}
	if preferences != nil && preferences.Value != "" {
		if err := json.Unmarshal([]byte(preferences.Value), &preferenceData); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Unable to prepare data export"})
			return
		}
	}
	data, err := h.userRepo.LoadUserDataExport(c.Request.Context(), user.ID, user.CircleID)
	if err != nil {
		logging.FromContext(c).Errorw("account.handler.exportMyData failed to prepare export", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Unable to prepare data export"})
		return
	}
	document := gin.H{
		"format":     "dydan-user-export/v1",
		"exportedAt": time.Now().UTC().Format(time.RFC3339),
		"account": gin.H{
			"id": user.ID, "username": user.Username, "displayName": user.DisplayName,
			"email": user.Email, "timezone": user.Timezone, "circleId": user.CircleID,
			"userType": user.UserType,
		},
		"preferences": preferenceData,
		"data":        data,
	}
	c.Header("Content-Disposition", `attachment; filename="dydan-my-data.json"`)
	c.Header("Cache-Control", "no-store, private")
	c.JSON(http.StatusOK, document)
}

// importMyData restores the portable, user-scoped document produced by
// exportMyData. The document is verified to belong to the authenticated user
// (id and email match) before any field is applied; credentials and security
// material are never exported in the first place, so a restore can only ever
// write the user's own preferences back to their own account.
func (h *Handler) importMyData(c *gin.Context) {
	user, ok := auth.CurrentUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Authentication required"})
		return
	}
	var payload struct {
		Format  string `json:"format"`
		Account *struct {
			ID    int    `json:"id"`
			Email string `json:"email"`
		} `json:"account"`
		Preferences json.RawMessage `json:"preferences"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil || payload.Format != "dydan-user-export/v1" || payload.Account == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid data export file"})
		return
	}
	if payload.Account.ID != user.ID || !strings.EqualFold(strings.TrimSpace(payload.Account.Email), user.Email) {
		c.JSON(http.StatusForbidden, gin.H{"error": "This export file belongs to a different account"})
		return
	}
	if len(payload.Preferences) == 0 || string(payload.Preferences) == "null" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "The export file contains no preferences to restore"})
		return
	}
	var preferences Preferences
	if err := json.Unmarshal(payload.Preferences, &preferences); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "The export file is not a valid dydan export"})
		return
	}
	if err := validatePreferences(preferences); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "The export file contains invalid preferences"})
		return
	}
	encoded, err := json.Marshal(preferences)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Unable to restore your data"})
		return
	}
	if err := h.userRepo.SaveUserPreferences(c.Request.Context(), &uModel.UserPreference{UserID: user.ID, Value: string(encoded)}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Unable to restore your data"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"res": preferences, "restored": "preferences"})
}

// getIntegrationStatus reports which external integrations this server actually
// exposes to the requesting account. It is read-only and capability-driven:
// integrations whose credentials/configuration are absent are reported as not
// available instead of showing a connect switch that would silently discard.
func (h *Handler) getIntegrationStatus(c *gin.Context) {
	user, ok := auth.CurrentUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Authentication required"})
		return
	}
	webhookEditable := false
	if user.IsPlusMember() {
		if admins, err := h.circleRepo.GetCircleAdmins(c, user.CircleID); err == nil {
			for _, admin := range admins {
				if admin.ID == user.ID {
					webhookEditable = true
					break
				}
			}
		}
	}
	var webhookURL string
	if user.WebhookURL != nil {
		webhookURL = *user.WebhookURL
	}
	scheme := "http"
	if c.Request.TLS != nil || strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	c.JSON(http.StatusOK, gin.H{
		"res": gin.H{
			"apiDocs": gin.H{
				"enabled": h.serveSwagger,
				"url":     scheme + "://" + c.Request.Host + "/swagger/index.html",
			},
			"googleCalendar": gin.H{"available": false},
			"caldav":         gin.H{"available": false},
			"alexa":          gin.H{"available": false},
			"legal": gin.H{
				"privacyPolicyUrl": h.legal.PrivacyPolicyURL,
				"termsUrl":         h.legal.TermsURL,
			},
			"billing": gin.H{
				"invoiceHistory": h.billingEnabled,
			},
			"webhooks": gin.H{
				"available": true,
				"url":       webhookURL,
				"editable":  webhookEditable,
				"plusOnly":  !user.IsPlusMember(),
			},
		},
	})
}

// reportProblem accepts the diagnostic submitted by the approved "Report this
// problem" control on the error screen and persists it against the
// authenticated account. The request body may only describe the problem; the
// owning user, circle, user agent, and request path are stamped server-side so
// a report can never be filed against another account, and no credential or
// message content is accepted from the client.
func (h *Handler) reportProblem(c *gin.Context) {
	user, ok := auth.CurrentUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Authentication required"})
		return
	}
	var payload struct {
		Summary string `json:"summary" binding:"required,max=200"`
		Details string `json:"details" binding:"max=20000"`
		Screen  string `json:"screen" binding:"max=64"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "An error summary is required"})
		return
	}
	summary := strings.TrimSpace(payload.Summary)
	if summary == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "An error summary is required"})
		return
	}
	report := &uModel.UserErrorReport{
		UserID:    user.ID,
		CircleID:  user.CircleID,
		Summary:   summary,
		Details:   payload.Details,
		Screen:    strings.TrimSpace(payload.Screen),
		UserAgent: c.GetHeader("User-Agent"),
		Path:      c.Request.URL.Path,
		CreatedAt: time.Now().UTC(),
	}
	if err := h.userRepo.SaveErrorReport(c.Request.Context(), report); err != nil {
		logging.FromContext(c).Errorw("failed to store client error report", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Unable to record this report"})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"res": gin.H{"id": report.ID, "recordedAt": report.CreatedAt}})
}

// startEmailChange requires fresh knowledge of the current credential before
// issuing a one-time proof-of-control link to the replacement address. The
// account email remains unchanged until that link is consumed.
func (h *Handler) startEmailChange(c *gin.Context) {
	currentUser, ok := auth.CurrentUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Authentication required"})
		return
	}
	var request struct {
		Email           string `json:"email" binding:"required,max=254"`
		CurrentPassword string `json:"currentPassword" binding:"required,max=128"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Enter a valid new email address and your current password"})
		return
	}
	address := strings.ToLower(strings.TrimSpace(request.Email))
	parsed, err := mail.ParseAddress(address)
	if err != nil || parsed.Address != address {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Enter a valid new email address"})
		return
	}
	if address == strings.ToLower(strings.TrimSpace(currentUser.Email)) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Choose an email address different from your current one"})
		return
	}
	duplicate, err := h.userRepo.EmailInUseByOther(c.Request.Context(), currentUser.ID, address)
	if err != nil {
		logging.FromContext(c).Errorw("account.handler.startEmailChange failed to check destination", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Unable to start email change"})
		return
	}
	if duplicate {
		c.JSON(http.StatusConflict, gin.H{"error": "Another account already uses this email address"})
		return
	}
	if err := auth.Matches(currentUser.Password, request.CurrentPassword); err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "Current password is incorrect"})
		return
	}
	token, err := auth.GenerateEmailResetToken(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Unable to start email change"})
		return
	}
	if err := h.userRepo.CreateEmailChangeTransaction(c.Request.Context(), currentUser.ID, address, token); err != nil {
		logging.FromContext(c).Errorw("account.handler.startEmailChange failed to persist transaction", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Unable to start email change"})
		return
	}
	sender, ok := h.email.(email.EmailChangeSender)
	if !ok {
		_ = h.userRepo.DeleteEmailChangeTransaction(c.Request.Context(), token)
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Verified email delivery is unavailable"})
		return
	}
	if err := sender.SendEmailChangeVerification(c.Request.Context(), address, token); err != nil {
		_ = h.userRepo.DeleteEmailChangeTransaction(c.Request.Context(), token)
		logging.FromContext(c).Errorw("account.handler.startEmailChange failed to send verification", "err", err)
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Could not send the verification email. Check delivery settings and try again."})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"message": "Verification email sent", "expiresInSeconds": int(uRepo.EmailChangeLifetime.Seconds())})
}

// verifyEmailChange is intentionally unauthenticated: possession of the
// short-lived, single-use link proves control of the new address. It never
// accepts an account identifier from the browser.
func (h *Handler) verifyEmailChange(c *gin.Context) {
	var request struct {
		Token string `json:"token" binding:"required,max=512"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid email change link"})
		return
	}
	if err := h.userRepo.ConsumeEmailChangeTransaction(c.Request.Context(), request.Token); err != nil {
		switch {
		case errors.Is(err, uRepo.ErrInvalidEmailChangeToken):
			c.JSON(http.StatusBadRequest, gin.H{"error": "This email change link is invalid, expired, or has already been used"})
		case errors.Is(err, uRepo.ErrEmailAlreadyInUse):
			c.JSON(http.StatusConflict, gin.H{"error": "Another account already uses this email address"})
		default:
			logging.FromContext(c).Errorw("account.handler.verifyEmailChange failed", "err", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Unable to confirm email change"})
		}
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Email address updated. Please sign in again."})
}

func (h *Handler) CreateLongLivedToken(c *gin.Context) {
	currentUser, ok := auth.CurrentUser(c)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get current user"})
		return
	}

	type TokenRequest struct {
		Name    string `json:"name" binding:"required"`
		MFACode string `json:"mfaCode"` // Optional MFA code for enhanced security
	}
	var req TokenRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request: " + err.Error()})
		return
	}

	// API tokens are long-lived credentials. An MFA-enabled account must prove
	// possession of its second factor before a new one can be minted.
	if currentUser.MFAEnabled && req.MFACode == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "MFA code is required to create an API token"})
		return
	}
	if currentUser.MFAEnabled {
		valid, newUsedCodes, err := h.mfaService.IsCodeValid(
			currentUser.MFASecret,
			currentUser.MFABackupCodes,
			currentUser.MFARecoveryUsed,
			req.MFACode,
		)

		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to validate MFA code"})
			return
		}

		if !valid {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid MFA code"})
			return
		}

		// Update used codes if a backup code was used
		if newUsedCodes != currentUser.MFARecoveryUsed {
			if err := h.userRepo.UpdateMFARecoveryCodes(c, currentUser.ID, newUsedCodes); err != nil {
				logging.FromContext(c).Errorw("Failed to update recovery codes", "error", err)
			}
		}
	}

	// Step 1: Generate a secure random number
	randomBytes := make([]byte, 16) // 128 bits are enough for strong randomness
	_, err := rand.Read(randomBytes)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate random part of the token"})
		return
	}

	timestamp := time.Now().UTC().Unix()
	hashInput := fmt.Sprintf("%s:%d:%x", currentUser.Username, timestamp, randomBytes)
	hash := sha256.Sum256([]byte(hashInput))
	token := hex.EncodeToString(hash[:])

	tokenModel, err := h.userRepo.StoreAPIToken(c, currentUser.ID, req.Name, token)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to store the token"})
		return
	}

	// The raw credential is disclosed exactly once, here. Listing and other
	// read paths only ever expose safe metadata (see APIToken model).
	response := gin.H{
		"res": gin.H{
			"id":        tokenModel.ID,
			"name":      tokenModel.Name,
			"createdAt": tokenModel.CreatedAt,
			"token":     token,
		},
	}

	c.JSON(http.StatusOK, response)
}

func (h *Handler) GetAllUserToken(c *gin.Context) {
	currentUser, ok := auth.CurrentUser(c)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get current user"})
		return
	}

	tokens, err := h.userRepo.GetAllUserTokens(c, currentUser.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get user tokens"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"res": tokens})

}

func (h *Handler) DeleteUserToken(c *gin.Context) {
	currentUser, ok := auth.CurrentUser(c)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get current user"})
		return
	}

	tokenID := c.Param("id")

	err := h.userRepo.DeleteAPIToken(c, currentUser.ID, tokenID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete the token"})
		return
	}

	c.JSON(http.StatusOK, gin.H{})
}

func (h *Handler) UpdateNotificationTarget(c *gin.Context) {
	currentUser, ok := auth.CurrentUser(c)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get current user"})
		return
	}

	type Request struct {
		Type   nModel.NotificationPlatform `json:"type"`
		Target string                      `json:"target"`
	}

	var req Request
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request: " + err.Error()})
		return
	}
	if req.Type == nModel.NotificationPlatformNone {
		err := h.userRepo.DeleteNotificationTarget(c, currentUser.ID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete notification target"})
			return
		}
		c.JSON(http.StatusOK, gin.H{})
		return
	}

	err := h.userRepo.UpdateNotificationTarget(c, currentUser.ID, req.Target, req.Type)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update notification target"})
		return
	}

	err = h.userRepo.UpdateNotificationTargetForAllNotifications(c, currentUser.ID, req.Target, req.Type)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update notification target for all notifications"})
		return
	}

	c.JSON(http.StatusOK, gin.H{})
}

// updateUserPasswordLoggedInOnly handles an authenticated user changing their
// own password. The current password is mandatory and verified server-side;
// failures return a uniform error that does not hint at how close the
// submitted value was. On success every other session of the user is revoked
// so other devices must re-authenticate, while the device performing the
// change keeps its current session.
func (h *Handler) updateUserPasswordLoggedInOnly(c *gin.Context) {
	if h.isDydanCloud {
		// only enable this feature for self-hosted instances
		c.JSON(http.StatusForbidden, gin.H{"error": "This action is not allowed on the hosted Dydan service"})
		return
	}
	logger := logging.FromContext(c)
	type RequestBody struct {
		Password        string `json:"password" binding:"required,min=8,max=64"`
		CurrentPassword string `json:"current_password" binding:"required"`
		// RefreshToken optionally identifies the caller's own session for
		// native clients that authenticate via Bearer and have no cookie.
		RefreshToken string `json:"refresh_token"`
	}
	var body RequestBody
	if err := c.ShouldBindJSON(&body); err != nil {
		logger.Errorw("user.handler.changePassword failed to bind", "err", err)
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Invalid request",
		})
		return
	}

	currentUser, ok := auth.CurrentUser(c)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get current user"})
		return
	}

	storedUser, err := h.userRepo.GetUserByID(c.Request.Context(), currentUser.ID)
	if err != nil {
		logger.Errorw("account.handler.changePassword failed to load user", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Unable to change password"})
		return
	}

	// Verify the current password before accepting any change.
	if authErr := auth.Matches(storedUser.Password, body.CurrentPassword); authErr != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "Current password is incorrect"})
		return
	}

	password, err := auth.EncodePassword(body.Password)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Unable to process password",
		})
		return
	}

	// Preserve the authenticated access token's family, not one particular
	// refresh row: rotation may already have replaced the cookie/body ancestor.
	_, keepFamilyID := auth.SessionRefFromClaims(map[string]interface{}(jwt.ExtractClaims(c)))
	if keepFamilyID == "" && body.RefreshToken != "" {
		_, keepFamilyID = h.tokenService.SessionRefForToken(c.Request.Context(), body.RefreshToken)
	}
	err = h.userRepo.ChangePasswordAndRevokeOtherFamilies(c.Request.Context(), currentUser.ID, password, keepFamilyID)
	if err != nil {
		logger.Errorw("account.handler.changePassword failed to reset password", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Unable to reset password",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{})
}

func (h *Handler) setWebhook(c *gin.Context) {
	currentUser, ok := auth.CurrentUser(c)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get current user"})
		return
	}

	type Request struct {
		URL *string `json:"url"`
	}

	var req Request
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request: " + err.Error()})
		return
	}

	// Structural validation at persist time: the destination is dialed later
	// by the events worker under the shared outbound policy, but rejecting
	// malformed/non-HTTP(S) URLs here gives callers an explicit error instead
	// of silently dropped deliveries. Network-location policy (private-range
	// blocking / opt-in) remains enforced at dial time.
	if req.URL != nil && *req.URL != "" {
		if err := outbound.ValidateURLSyntax(*req.URL); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid webhook URL: " + err.Error()})
			return
		}
	}

	if !currentUser.IsPlusMember() {
		c.JSON(http.StatusForbidden, gin.H{"error": "This action is only available for Plus members"})
		return
	}

	// get circle admins
	admins, err := h.circleRepo.GetCircleAdmins(c, currentUser.CircleID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get circle details"})
		return
	}

	// confirm that the user is an admin:
	isAdmin := false
	for _, admin := range admins {
		if admin.ID == currentUser.ID {
			isAdmin = true
			break
		}
	}
	if !isAdmin {
		c.JSON(http.StatusForbidden, gin.H{"error": "You are not an admin"})
		return
	}

	err = h.circleRepo.SetWebhookURL(c, currentUser.CircleID, req.URL)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to set webhook URL"})
		return
	}
	c.JSON(http.StatusOK, gin.H{})
}

func (h *Handler) updateProfilePhoto(c *gin.Context) {
	currentUser, ok := auth.CurrentUser(c)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get current user"})
		return
	}

	file, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid file"})
		return
	}
	// Use filepath.Ext so dotless filenames return "" instead of panicking on
	// a negative slice index.
	fileExtension := strings.ToLower(filepath.Ext(file.Filename))
	// validate file extension:
	if fileExtension != ".jpg" && fileExtension != ".jpeg" && fileExtension != ".png" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid file extension"})
		return
	}

	// Use a random UUID for the filename so the path is not guessable from
	// the username — this matters when the bucket is configured for public
	// reads (storage.public_read: true) and the URL is served unsigned.
	// UPDATE: we add back userID but kept the UUID to avoid collisions and make it unguessable.
	// the currentUser.ID help managing the storage usage for each user

	filename := fmt.Sprintf("profiles/%d/%s%s", currentUser.ID, uuid.New().String(), fileExtension)

	openedFile, err := file.Open()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to open file"})
		return
	}
	defer openedFile.Close()

	err = h.storage.SavePublic(c, filename, openedFile)
	if err != nil {
		logging.FromContext(c).Errorw("Failed to save profile photo", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save file"})
		return
	}

	// Resolve the URL before persisting. For S3 with a public bucket host this
	// is a permanent bare URL; for local or unconfigured public bucket it is a
	// raw path that SignIfLocal will sign on each read.
	photoURL, err := h.storage.GetPublicURL(c, filename)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to resolve profile photo URL"})
		return
	}

	// Clean up the previous profile photo when it is a locally-stored path.
	// Full https:// URLs (OIDC picture claims or public-bucket URLs) are
	// skipped — public-bucket objects accumulate until a future cleanup pass.
	if prev := currentUser.Image; prev != "" &&
		!strings.HasPrefix(prev, "http://") &&
		!strings.HasPrefix(prev, "https://") {
		// Legacy rows stored the storage base directory in the path
		// (e.g. "uploads/profiles/..."); storage.Delete expects a key
		// relative to BasePath.
		if idx := strings.Index(prev, "profiles/"); idx > 0 {
			prev = prev[idx:]
		}
		if derr := h.storage.Delete(c, []string{prev}); derr != nil {
			logging.FromContext(c).Warnw("Failed to delete previous profile photo", "path", prev, "error", derr)
		}
	}

	err = h.userRepo.UpdateUserImage(c, currentUser.ID, photoURL)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update profile photo"})
		return
	}
	// SignIfLocal passes through https:// URLs unchanged, so the response is
	// correct regardless of whether a public bucket host is configured.
	c.JSON(http.StatusOK, gin.H{"sign": h.signer.SignIfLocal(photoURL)})
}

func (h *Handler) getStorageUsage(c *gin.Context) {
	currentUser, ok := auth.CurrentUser(c)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get current user"})
		return
	}

	used, available, err := h.storageRepo.GetStorageStats(c, currentUser)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get storage usage"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"res": gin.H{
			"used":  used,
			"total": available,
		},
	})

}

// Account deletion request/response types
type AccountDeletionRequest struct {
	Password        string                 `json:"password" binding:"required"`
	TransferOptions []CircleTransferOption `json:"transferOptions,omitempty"`
	Confirmation    string                 `json:"confirmation" binding:"required"` // Must be "DELETE"
}

type AccountDeletionCheckRequest struct {
	Password string `json:"password" binding:"required"`
}

func (h *Handler) checkAccountDeletion(c *gin.Context) {
	currentUser, ok := auth.CurrentUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	var req AccountDeletionCheckRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Verify password
	if auth.Matches(currentUser.Password, req.Password) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid password"})
		return
	}

	// Check what would be deleted (dry run)
	result, err := h.deletionService.CheckUserAccountDeletion(c.Request.Context(), currentUser.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to check account deletion: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, result)
}

func (h *Handler) deleteAccount(c *gin.Context) {
	currentUser, ok := auth.CurrentUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	var req AccountDeletionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Validate confirmation text
	if req.Confirmation != "DELETE" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Confirmation text must be 'DELETE'"})
		return
	}

	// Verify password
	if auth.Matches(currentUser.Password, req.Password) != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid password"})
		return
	}

	// Perform account deletion
	result, err := h.deletionService.DeleteUserAccount(c.Request.Context(), currentUser.ID, req.TransferOptions)
	if err != nil {
		logging.DefaultLogger().Errorf("Failed to delete account for user %d: %v", currentUser.ID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete account: " + err.Error()})
		return
	}

	if !result.Success {
		c.JSON(http.StatusBadRequest, result)
		return
	}

	// Log the account deletion
	logging.DefaultLogger().Infof("Account deleted successfully for user %d (%s)", currentUser.ID, currentUser.Username)

	c.JSON(http.StatusOK, result)
}

// Child User Management endpoints

// CreateChildUserRequest represents the request to create a child user
type CreateChildUserRequest struct {
	ChildName   string `json:"childName" binding:"required,min=2,max=20"`
	DisplayName string `json:"displayName"`
	Password    string `json:"password" binding:"required,min=8,max=64"`
}

// UpdateChildPasswordRequest represents the request to update a child user's password
type UpdateChildPasswordRequest struct {
	ChildUserID int    `json:"childUserId" binding:"required"`
	Password    string `json:"password" binding:"required,min=8,max=64"`
}

// ChildUserResponse represents the response for child user operations
type ChildUserResponse struct {
	ID          int    `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
	UserType    string `json:"userType"`
	CreatedAt   string `json:"createdAt"`
}

func (h *Handler) createChildUser(c *gin.Context) {
	currentUser, ok := auth.CurrentUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	// Only parent users can create child users
	if currentUser.UserType != uModel.UserTypeParent {
		c.JSON(http.StatusForbidden, gin.H{"error": "Only parent users can create child accounts"})
		return
	}

	var req CreateChildUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Check current child user count to enforce the hosted Dydan service limit.
	if h.isDydanCloud {
		currentChildCount, err := h.userRepo.GetChildUserCount(c, currentUser.ID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to check existing subaccounts"})
			return
		}

		maxSubaccounts := h.maxSubaccounts
		if currentUser.IsPlusMember() {
			maxSubaccounts = h.plusMaxSubaccounts
		}

		if int(currentChildCount) >= maxSubaccounts {
			c.JSON(http.StatusForbidden, gin.H{
				"error": fmt.Sprintf("Maximum of %d subaccounts allowed per account", maxSubaccounts),
			})
			return
		}
	}
	// Validate username and child username:
	if !utils.IsValidUsername(currentUser.Username) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid parent username format"})
		return
	}
	if !utils.IsValidUsername(req.ChildName) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid child username format"})
		return
	}

	// Generate child username
	childUsername := uModel.GenerateChildUsername(currentUser.Username, req.ChildName)

	// Set default display name if not provided
	if req.DisplayName == "" {
		req.DisplayName = req.ChildName
	}

	// Encode password
	encodedPassword, err := auth.EncodePassword(req.Password)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to process password"})
		return
	}

	// Create child user
	childUser := &uModel.User{
		Username:     childUsername,
		DisplayName:  html.EscapeString(req.DisplayName),
		Password:     encodedPassword,
		CircleID:     currentUser.CircleID,
		ParentUserID: &currentUser.ID,
		UserType:     uModel.UserTypeChild,
		Provider:     uModel.AuthProviderDydan,
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
	}

	// Validate child user
	if err := childUser.ValidateChildUser(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	createdUser, err := h.userRepo.CreateChildWithMembership(c, childUser, &cModel.UserCircle{
		CircleID:  currentUser.CircleID,
		Role:      "member", // Child users are members, not admins
		IsActive:  true,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create child user"})
		return
	}

	response := ChildUserResponse{
		ID:          createdUser.ID,
		Username:    createdUser.Username,
		DisplayName: createdUser.DisplayName,
		UserType:    "child",
		CreatedAt:   createdUser.CreatedAt.Format(time.RFC3339),
	}

	c.JSON(http.StatusCreated, gin.H{"res": response})
}

func (h *Handler) updateChildPassword(c *gin.Context) {
	currentUser, ok := auth.CurrentUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	// Only parent users can update child passwords
	if currentUser.UserType != uModel.UserTypeParent {
		c.JSON(http.StatusForbidden, gin.H{"error": "Only parent users can update child passwords"})
		return
	}

	var req UpdateChildPasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Verify that the child user belongs to the current parent
	childUser, err := h.userRepo.GetUserByID(c, req.ChildUserID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Child user not found"})
		return
	}

	if childUser.ParentUserID == nil || *childUser.ParentUserID != currentUser.ID {
		c.JSON(http.StatusForbidden, gin.H{"error": "You can only update passwords for your own child users"})
		return
	}

	// Encode new password
	encodedPassword, err := auth.EncodePassword(req.Password)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to process password"})
		return
	}

	// Update password
	err = h.userRepo.ChangeChildPasswordAndRevokeSessions(c.Request.Context(), req.ChildUserID, encodedPassword)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update password"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Child user password updated successfully"})
}

func (h *Handler) deleteChildUser(c *gin.Context) {
	currentUser, ok := auth.CurrentUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	// Only parent users can delete child users
	if currentUser.UserType != uModel.UserTypeParent {
		c.JSON(http.StatusForbidden, gin.H{"error": "Only parent users can delete child accounts"})
		return
	}

	childUserID := c.Param("id")
	if childUserID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Child user ID is required"})
		return
	}

	// Parse child user ID
	childID := 0
	if _, err := fmt.Sscanf(childUserID, "%d", &childID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid child user ID"})
		return
	}

	// Verify that the child user belongs to the current parent
	childUser, err := h.userRepo.GetUserByID(c, childID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Child user not found"})
		return
	}

	if childUser.ParentUserID == nil || *childUser.ParentUserID != currentUser.ID {
		c.JSON(http.StatusForbidden, gin.H{"error": "You can only delete your own child users"})
		return
	}

	// Delete child user account (this will cascade delete all associated data)
	result, err := h.deletionService.DeleteUserAccount(c.Request.Context(), childID, nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete child user: " + err.Error()})
		return
	}

	if !result.Success {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete child user"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Child user deleted successfully"})
}

func (h *Handler) getChildUsers(c *gin.Context) {
	currentUser, ok := auth.CurrentUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	// Only parent users can view child users
	if currentUser.UserType != uModel.UserTypeParent {
		c.JSON(http.StatusForbidden, gin.H{"error": "Only parent users can view child accounts"})
		return
	}

	childUsers, err := h.userRepo.GetChildUsersByParentID(c, currentUser.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get child users"})
		return
	}

	var response []ChildUserResponse
	for _, child := range childUsers {
		response = append(response, ChildUserResponse{
			ID:          child.ID,
			Username:    child.Username,
			DisplayName: child.DisplayName,
			UserType:    "child",
			CreatedAt:   child.CreatedAt.Format(time.RFC3339),
		})
	}

	c.JSON(http.StatusOK, gin.H{"res": response})
}

// passwordAuthDisabled rejects password-based auth requests (signup, login,
// password reset) when the instance is configured as SSO-only via
// disable_password_auth (#438).
func passwordAuthDisabled() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
			"error": "Password authentication is disabled on this instance. Please sign in with SSO.",
		})
	}
}

func (h *Handler) requirePermission(permission auth.Permission) gin.HandlerFunc {
	return func(c *gin.Context) {
		currentUser, ok := auth.CurrentUser(c)
		if !ok {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
			return
		}
		circleRole, err := h.circleRepo.GetActiveUserCircleRole(c, currentUser.CircleID, currentUser.ID)
		role := auth.ResolveEffectiveRole(&currentUser.User, circleRole, err == nil)
		if err != nil || !auth.HasPermission(role, permission) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "You do not have permission to manage this setting"})
			return
		}
		c.Next()
	}
}

func Routes(router *gin.Engine, h *Handler, jwtAuth *jwt.GinJWTMiddleware, limiter *limiter.Limiter, securityLimiter *authlimit.Limiter, cfg *config.Config) {

	// Origin validation for cookie-authenticated endpoints (refresh/logout).
	// Forwarding headers are only honored from cfg.Server.TrustedProxies so
	// directly exposed instances cannot be spoofed.
	csrfCheck := auth.CSRFOriginCheck(cfg.Server.CorsAllowOrigins, cfg.Server.TrustedProxies)
	// Tighter budget for credential-changing actions than the general API
	// bucket, so brute-forcing the current-password check is impractical even
	// from a fast connection.
	pwChangeLimiter := utils.NewBoundedRateLimiter(cfg.Server.RateLimitPasswordChangePeriod, int64(cfg.Server.RateLimitPasswordChange))
	authLimiter := utils.NewBoundedRateLimiter(cfg.Server.RateLimitAuthPeriod, int64(cfg.Server.RateLimitAuth))
	recoveryLimiter := utils.NewBoundedRateLimiter(cfg.Server.RateLimitRecoveryPeriod, int64(cfg.Server.RateLimitRecovery))
	authHandler := auth.NewAuthHandler(h.tokenService, h.userRepo, jwtAuth, h.mfaService)
	authHandler.SetCSRFCheck(csrfCheck)

	userRoutes := router.Group("api/v1/users")
	userRoutes.Use(jwtAuth.MiddlewareFunc(), utils.RateLimitMiddleware(limiter), h.verboseRequestLogger())
	{
		userRoutes.GET("/", h.GetAllUsers())
		userRoutes.GET("/profile", h.GetUserProfile)
		userRoutes.GET("/export", h.requirePermission(auth.PermissionManageAccount), h.exportMyData)
		userRoutes.POST("/import", h.requirePermission(auth.PermissionManageAccount), h.importMyData)
		userRoutes.GET("/integrations", h.requirePermission(auth.PermissionManageAccount), h.getIntegrationStatus)
		userRoutes.POST("/error-reports", h.requirePermission(auth.PermissionManageAccount), h.reportProblem)
		userRoutes.PUT("", h.UpdateUserDetails)
		userRoutes.GET("/preferences", h.requirePermission(auth.PermissionManageAccount), h.getPreferences)
		userRoutes.PUT("/preferences", h.requirePermission(auth.PermissionManageAccount), h.updatePreferences)
		userRoutes.POST("/preferences/reset", h.requirePermission(auth.PermissionManageAccount), h.resetPreferences)
		userRoutes.POST("/tokens", h.requirePermission(auth.PermissionManageAccount), securityLimiter.Middleware("token-create", cfg.Server.RateLimitAuthPeriod, int64(cfg.Server.RateLimitAuth)), h.CreateLongLivedToken)
		userRoutes.GET("/tokens", h.requirePermission(auth.PermissionManageAccount), h.GetAllUserToken)
		userRoutes.DELETE("/tokens/:id", h.requirePermission(auth.PermissionManageAccount), h.DeleteUserToken)
		userRoutes.PUT("/webhook", h.requirePermission(auth.PermissionManageCircle), h.setWebhook)
		userRoutes.PUT("/targets", h.UpdateNotificationTarget)
		userRoutes.PUT("change_password", securityLimiter.Middleware("password-change", cfg.Server.RateLimitPasswordChangePeriod, int64(cfg.Server.RateLimitPasswordChange)), utils.RateLimitMiddleware(pwChangeLimiter), h.updateUserPasswordLoggedInOnly)
		userRoutes.POST("/email-change", securityLimiter.Middleware("email-change-start", cfg.Server.RateLimitPasswordChangePeriod, int64(cfg.Server.RateLimitPasswordChange)), h.startEmailChange)
		userRoutes.POST("profile_photo", h.updateProfilePhoto)
		userRoutes.GET("storage", h.getStorageUsage)
		userRoutes.POST("/logout", securityLimiter.Middleware("logout", cfg.Server.RateLimitAuthPeriod, int64(cfg.Server.RateLimitAuth), authlimit.AccountField{Name: "refresh_token"}), authHandler.LogoutHandler)

		// MFA endpoints
		userRoutes.GET("/mfa/status", h.requirePermission(auth.PermissionManageAccount), h.getMFAStatus)
		userRoutes.POST("/mfa/setup", h.requirePermission(auth.PermissionManageAccount), h.setupMFA)
		userRoutes.POST("/mfa/confirm", securityLimiter.Middleware("mfa-confirm", cfg.Server.RateLimitAuthPeriod, int64(cfg.Server.RateLimitAuth)), h.requirePermission(auth.PermissionManageAccount), h.confirmMFA)
		userRoutes.POST("/mfa/disable", securityLimiter.Middleware("mfa-disable", cfg.Server.RateLimitAuthPeriod, int64(cfg.Server.RateLimitAuth)), h.requirePermission(auth.PermissionManageAccount), h.disableMFA)
		userRoutes.POST("/mfa/regenerate-backup-codes", securityLimiter.Middleware("mfa-regenerate-backup-codes", cfg.Server.RateLimitAuthPeriod, int64(cfg.Server.RateLimitAuth)), h.requirePermission(auth.PermissionManageAccount), h.regenerateBackupCodes)

		// Account deletion endpoints
		userRoutes.POST("/delete/check", h.requirePermission(auth.PermissionManageAccount), h.checkAccountDeletion)
		userRoutes.DELETE("/delete", h.requirePermission(auth.PermissionManageAccount), h.deleteAccount)

		// Child user management endpoints
		userRoutes.POST("/subaccounts", h.requirePermission(auth.PermissionManageSubaccounts), h.createChildUser)
		userRoutes.GET("/subaccounts", h.requirePermission(auth.PermissionManageSubaccounts), h.getChildUsers)
		userRoutes.PUT("/subaccounts/password", securityLimiter.Middleware("child-password-change", cfg.Server.RateLimitPasswordChangePeriod, int64(cfg.Server.RateLimitPasswordChange)), h.requirePermission(auth.PermissionManageSubaccounts), h.updateChildPassword)
		userRoutes.DELETE("/subaccounts/:id", h.requirePermission(auth.PermissionManageSubaccounts), h.deleteChildUser)
	}
	publicUserRoutes := router.Group("api/v1/users")
	publicUserRoutes.POST("/email-change/verify", securityLimiter.Middleware("email-change-verify", cfg.Server.RateLimitAuthPeriod, int64(cfg.Server.RateLimitAuth)), h.verifyEmailChange)

	authRoutes := router.Group("api/v1/auth")
	authRoutes.Use(utils.RateLimitMiddleware(limiter))
	// Credential payloads are tiny; cap the body so oversized requests are
	// rejected before any limiter or validator buffers them (memory-DoS
	// guard). File uploads do not traverse this group.
	authRoutes.Use(auth.LimitRequestBody(auth.MaxAuthRequestBodyBytes))
	{
		authRoutes.POST("/:provider/callback", securityLimiter.Middleware("oidc-callback", cfg.Server.RateLimitAuthPeriod, int64(cfg.Server.RateLimitAuth)), h.thirdPartyAuthCallback)
		// OAuth2 login bootstrap: issues a single-use state bound to a PKCE
		// verifier (S256) and the validated redirect URI. Registered under the
		// same ":provider" segment as /callback because gin's router tree does
		// not allow mixing static and param segments at the same depth.
		authRoutes.POST("/:provider/start", securityLimiter.Middleware("oidc-start", cfg.Server.RateLimitAuthPeriod, int64(cfg.Server.RateLimitAuth)), h.thirdPartyAuthStart)
		authRoutes.POST("refresh", securityLimiter.Middleware("refresh", cfg.Server.RateLimitAuthPeriod, int64(cfg.Server.RateLimitAuth), authlimit.AccountField{Name: "refresh_token"}), authHandler.RefreshTokenHandler)
		authRoutes.POST("logout", securityLimiter.Middleware("logout", cfg.Server.RateLimitAuthPeriod, int64(cfg.Server.RateLimitAuth), authlimit.AccountField{Name: "refresh_token"}), authHandler.LogoutHandler)
		authRoutes.POST("mfa/verify", securityLimiter.Middleware("mfa-login", cfg.Server.RateLimitAuthPeriod, int64(cfg.Server.RateLimitAuth), authlimit.AccountField{Name: "sessionToken"}), utils.RateLimitMiddleware(authLimiter), h.verifyMFA)

		// Password-based auth (signup, login, password reset). When
		// disable_password_auth is set the instance is SSO-only, so these
		// routes are guarded and reject with 403 (#438).
		pwAuth := authRoutes.Group("")
		if cfg.DisablePasswordAuth {
			pwAuth.Use(passwordAuthDisabled())
		}
		pwAuth.POST("/", securityLimiter.Middleware("signup", cfg.Server.RateLimitAuthPeriod, int64(cfg.Server.RateLimitAuth), authlimit.AccountField{Name: "username", CaseInsensitive: true}, authlimit.AccountField{Name: "email", CaseInsensitive: true}), utils.RateLimitMiddleware(authLimiter), h.signUp)
		pwAuth.POST("login", securityLimiter.Middleware("login", cfg.Server.RateLimitAuthPeriod, int64(cfg.Server.RateLimitAuth), authlimit.AccountField{Name: "username", CaseInsensitive: true}), utils.RateLimitMiddleware(authLimiter), authHandler.EnhancedLoginHandler)
		pwAuth.POST("login/legacy", securityLimiter.Middleware("legacy-login", cfg.Server.RateLimitAuthPeriod, int64(cfg.Server.RateLimitAuth), authlimit.AccountField{Name: "username", CaseInsensitive: true}), utils.RateLimitMiddleware(authLimiter), jwtAuth.LoginHandler)
		pwAuth.POST("reset", securityLimiter.Middleware("recovery-request", cfg.Server.RateLimitRecoveryPeriod, int64(cfg.Server.RateLimitRecovery), authlimit.AccountField{Name: "email", CaseInsensitive: true}), utils.RateLimitMiddleware(recoveryLimiter), h.resetPassword)
		pwAuth.POST("password", securityLimiter.Middleware("recovery-redeem", cfg.Server.RateLimitAuthPeriod, int64(cfg.Server.RateLimitAuth), authlimit.AccountField{Name: "code"}), utils.RateLimitMiddleware(authLimiter), h.updateUserPassword)
	}

	// Protected auth routes (require JWT)
	protectedAuthRoutes := router.Group("api/v1/auth")
	protectedAuthRoutes.Use(jwtAuth.MiddlewareFunc(), utils.RateLimitMiddleware(limiter), auth.LimitRequestBody(auth.MaxAuthRequestBodyBytes))
	{
		protectedAuthRoutes.POST("revoke-all", authHandler.RevokeAllHandler) // New revoke all endpoint

		// Session management: list/revoke the caller's own login sessions.
		// Bearer-authenticated (no cookies), so no CSRF check applies; all
		// queries are scoped to the authenticated user server-side.
		protectedAuthRoutes.GET("sessions", authHandler.ListSessionsHandler)
		protectedAuthRoutes.DELETE("sessions/:id", authHandler.RevokeSessionHandler)
		protectedAuthRoutes.POST("sessions/revoke-others", authHandler.RevokeOtherSessionsHandler)
	}
}
