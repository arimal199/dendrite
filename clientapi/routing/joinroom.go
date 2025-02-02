// Copyright 2017 Vector Creations Ltd
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package routing

import (
	"net/http"
	"time"

	appserviceAPI "github.com/matrix-org/dendrite/appservice/api"
	"github.com/matrix-org/dendrite/clientapi/httputil"
	"github.com/matrix-org/dendrite/clientapi/jsonerror"
	roomserverAPI "github.com/matrix-org/dendrite/roomserver/api"
	"github.com/matrix-org/dendrite/userapi/api"
	"github.com/matrix-org/gomatrixserverlib"
	"github.com/matrix-org/util"
)

func JoinRoomByIDOrAlias(
	req *http.Request,
	device *api.Device,
	rsAPI roomserverAPI.ClientRoomserverAPI,
	profileAPI api.ClientUserAPI,
	roomIDOrAlias string,
) util.JSONResponse {
	// Prepare to ask the roomserver to perform the room join.
	joinReq := roomserverAPI.PerformJoinRequest{
		RoomIDOrAlias: roomIDOrAlias,
		UserID:        device.UserID,
		IsGuest:       device.AccountType == api.AccountTypeGuest,
		Content:       map[string]interface{}{},
	}
	joinRes := roomserverAPI.PerformJoinResponse{}

	// Check to see if any ?server_name= query parameters were
	// given in the request.
	if serverNames, ok := req.URL.Query()["server_name"]; ok {
		for _, serverName := range serverNames {
			joinReq.ServerNames = append(
				joinReq.ServerNames,
				gomatrixserverlib.ServerName(serverName),
			)
		}
	}

	// If content was provided in the request then include that
	// in the request. It'll get used as a part of the membership
	// event content.
	_ = httputil.UnmarshalJSONRequest(req, &joinReq.Content)

	// Work out our localpart for the client profile request.

	// Request our profile content to populate the request content with.
	profile, err := profileAPI.QueryProfile(req.Context(), device.UserID)

	switch err {
	case nil:
		joinReq.Content["displayname"] = profile.DisplayName
		joinReq.Content["avatar_url"] = profile.AvatarURL
	case appserviceAPI.ErrProfileNotExists:
		util.GetLogger(req.Context()).Error("Unable to query user profile, no profile found.")
		return util.JSONResponse{
			Code: http.StatusInternalServerError,
			JSON: jsonerror.Unknown("Unable to query user profile, no profile found."),
		}
	default:
	}

	// Ask the roomserver to perform the join.
	done := make(chan util.JSONResponse, 1)
	go func() {
		defer close(done)
		if err := rsAPI.PerformJoin(req.Context(), &joinReq, &joinRes); err != nil {
			done <- jsonerror.InternalAPIError(req.Context(), err)
		} else if joinRes.Error != nil {
			if joinRes.Error.Code == roomserverAPI.PerformErrorNotAllowed && device.AccountType == api.AccountTypeGuest {
				done <- util.JSONResponse{
					Code: http.StatusForbidden,
					JSON: jsonerror.GuestAccessForbidden(joinRes.Error.Msg),
				}
			} else {
				done <- joinRes.Error.JSONResponse()
			}
		} else {
			done <- util.JSONResponse{
				Code: http.StatusOK,
				// TODO: Put the response struct somewhere internal.
				JSON: struct {
					RoomID string `json:"room_id"`
				}{joinRes.RoomID},
			}
		}
	}()

	// Wait either for the join to finish, or for us to hit a reasonable
	// timeout, at which point we'll just return a 200 to placate clients.
	select {
	case <-time.After(time.Second * 20):
		return util.JSONResponse{
			Code: http.StatusAccepted,
			JSON: jsonerror.Unknown("The room join will continue in the background."),
		}
	case result := <-done:
		return result
	}
}
