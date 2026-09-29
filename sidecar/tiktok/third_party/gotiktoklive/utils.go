package gotiktoklive

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand"
	"time"

	"github.com/erni27/imcache"
	pb "github.com/steampoweredtaco/gotiktoklive/proto"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

const (
	messageHistoryTimeout = 15 * time.Minute
)

var (
	msgIDCache imcache.Cache[int64, struct{}]
)

func getRandomDeviceID() string {
	const chars = "0123456789"
	b := make([]byte, 20)
	for i := range b {
		b[i] = chars[rand.Intn(len(chars))]
	}
	return string(b)
}

func parseMsg(msg *pb.WebcastResponse_Message, warnHandler func(...interface{}), debugHandler func(...interface{}), enableExperimentalEvents bool) (out Event, err error) {
	tReflect, err := protoregistry.GlobalTypes.FindMessageByName(protoreflect.FullName(msg.Method))
	if err != nil {
		base := base64.RawStdEncoding.EncodeToString(msg.Payload)
		debugHandler(fmt.Sprintf("cannot find type %s:\n%s ", msg.Method, base))
		return nil, nil
	}
	m := tReflect.New().Interface()
	if err = proto.Unmarshal(msg.Payload, m); err != nil {
		base := base64.RawStdEncoding.EncodeToString(msg.Payload)
		err = fmt.Errorf("failed to unmarshal proto %T: %w\n%s", m, err, base)
		debugHandler(err)
		warnHandler(fmt.Errorf("failed to unmarshal proto %T: %w", m, err))
		return nil, nil
	}
	switch pt := m.(type) {
	case *pb.RoomMessage:
		return RoomEvent{
			MessageID: pt.Common.MsgId,
			Timestamp: pt.Common.CreateTime,
			Type:      pt.Common.Method,
			Message:   pt.Content,
			isHistory: msg.IsHistory || cachedHistory(pt.Common.MsgId),
		}, nil
	case *pb.WebcastRoomPinMessage:
		{
			tReflect, err := protoregistry.GlobalTypes.FindMessageByName(protoreflect.FullName(pt.OriginalMsgType))
			if err != nil {
				base := base64.RawStdEncoding.EncodeToString(msg.Payload)
				debugHandler("cannot find proto type for pin message %s:\n%s ", msg.Method, base)
				return RoomEvent{
					MessageID: msg.MsgId,
					Timestamp: pt.Common.CreateTime,
					Type:      pt.OriginalMsgType,
					Message:   "<unknown>",
					isHistory: msg.IsHistory || cachedHistory(pt.Common.MsgId),
				}, nil
			}
			m := tReflect.New().Interface()
			if err = proto.Unmarshal(pt.PinnedMessage, m); err != nil {
				base := base64.RawStdEncoding.EncodeToString(msg.Payload)
				err = fmt.Errorf("failed to unmarshal proto %T: %w\n%s", m, err, base)
				debugHandler(err)
				warnHandler(fmt.Errorf("failed to unmarshal proto %T: %w", m, err))
				return RoomEvent{
					MessageID: msg.MsgId,
					Timestamp: pt.Common.CreateTime,
					Type:      pt.OriginalMsgType,
					Message:   "<unknown>",
					isHistory: msg.IsHistory || cachedHistory(pt.Common.MsgId),
				}, nil
			}

			typeStr := pt.OriginalMsgType
			msgPinned := "<unknown pinned type>"
			switch pt2 := m.(type) {
			// Todo make a pin return type
			case *pb.WebcastChatMessage:
				return ChatEvent{
					MessageID: pt.Common.MsgId,
					Timestamp: pt.Common.CreateTime,
					Comment:   "<pinned>: " + pt2.Content,
					User:      toUser(pt2.User),
					isHistory: msg.IsHistory || cachedHistory(pt.Common.MsgId),
				}, nil
			default:
				base := base64.RawStdEncoding.EncodeToString(pt.PinnedMessage)
				err = fmt.Errorf("unimplemented pinned message type %T\n%s", m, base)
				debugHandler(err)
				warnHandler(fmt.Sprintf("unimplemented pinned message type %T", m))

			}
			return RoomEvent{
				MessageID: pt.Common.MsgId,
				Timestamp: pt.Common.CreateTime,
				Type:      typeStr,
				Message:   msgPinned,
				isHistory: msg.IsHistory || cachedHistory(pt.Common.MsgId),
			}, nil
		}
	case *pb.WebcastChatMessage:
		return ChatEvent{
			MessageID:    pt.Common.MsgId,
			Comment:      pt.Content,
			User:         toUser(pt.User),
			UserIdentity: toUserIdentity(pt.UserIdentity),
			Timestamp:    pt.Common.CreateTime,
			isHistory:    msg.IsHistory || cachedHistory(pt.Common.MsgId),
		}, nil
	case *pb.WebcastMemberMessage:
		return UserEvent{
			MessageID: pt.Common.MsgId,
			Timestamp: pt.Common.CreateTime,
			Event:     toUserType(pt.Action.String()),
			User:      toUser(pt.User),
			isHistory: msg.IsHistory || cachedHistory(pt.Common.MsgId),
		}, nil
	case *pb.WebcastLiveGameIntroMessage:
		return RoomEvent{
			MessageID: pt.Common.MsgId,
			Timestamp: pt.Common.CreateTime,
			Type:      pt.Common.Method,
			Message:   pt.GameText.DefaultPattern,
			isHistory: msg.IsHistory || cachedHistory(pt.Common.MsgId),
		}, nil
	case *pb.WebcastRoomMessage:
		return RoomEvent{
			MessageID: pt.Common.MsgId,
			Timestamp: pt.Common.CreateTime,
			Type:      pt.Common.Method,
			// TODO: Make this actually use pieces list and fill out the format text correctly.
			Message:   pt.Common.DisplayText.DefaultPattern,
			isHistory: msg.IsHistory || cachedHistory(pt.Common.MsgId),
		}, nil
	case *pb.WebcastRoomUserSeqMessage:
		return ViewersEvent{
			MessageID: pt.Common.MsgId,
			Timestamp: pt.Common.CreateTime,
			Viewers:   int(pt.Total),
			isHistory: msg.IsHistory || cachedHistory(pt.Common.MsgId),
		}, nil
	case *pb.WebcastSocialMessage:
		return UserEvent{
			MessageID: pt.Common.MsgId,
			Timestamp: pt.Common.CreateTime,
			Event:     toUserType(pt.Common.DisplayText.Key),
			User:      toUser(pt.User),
			isHistory: msg.IsHistory || cachedHistory(pt.Common.MsgId),
		}, nil
	case *pb.WebcastGiftMessage:
		if pt.GiftId == 0 && pt.User == nil {
			return nil, nil
		}

		// PATCH (upstream gap): surface the gift image. Prefer the largest
		// artwork (image > icon > giftLabelIcon); TikFinity exposes the same
		// value as giftPictureUrl.
		giftPic := ""
		for _, img := range []*pb.Image{pt.Gift.GetImage(), pt.Gift.GetIcon(), pt.Gift.GetGiftLabelIcon()} {
			if img != nil && len(img.UrlList) > 0 {
				giftPic = img.UrlList[len(img.UrlList)-1]
				break
			}
		}

		return GiftEvent{
			MessageID:    pt.Common.MsgId,
			Timestamp:    pt.Common.CreateTime,
			ID:           pt.GiftId,
			GroupID:      pt.GroupId,
			Name:         pt.Gift.Name,
			Describe:     pt.Gift.Describe,
			Diamonds:     int(pt.Gift.DiamondCount),
			RepeatCount:  int(pt.RepeatCount),
			RepeatEnd:    pt.RepeatEnd == 1,
			Type:         int(pt.Gift.Type),
			ToUserID:     toRecipientID(pt.UserGiftReciever),
			User:         toUser(pt.User),
			UserIdentity: toUserIdentity(pt.UserIdentity),
			isHistory:    msg.IsHistory || cachedHistory(pt.Common.MsgId),
			IsComboGift:  pt.GroupId != 0,
			PictureURL:   giftPic,
		}, nil
	case *pb.WebcastLikeMessage:
		return LikeEvent{
			MessageID:   pt.Common.MsgId,
			Timestamp:   pt.Common.CreateTime,
			Likes:       int(pt.Count),
			TotalLikes:  int(pt.Total),
			User:        toUser(pt.User),
			DisplayType: pt.Common.Method,
			Label:       pt.Common.DisplayText.String(),
			isHistory:   msg.IsHistory || cachedHistory(pt.Common.MsgId),
		}, nil

	case *pb.WebcastQuestionNewMessage:
		return QuestionEvent{
			MessageID: pt.Common.MsgId,
			Timestamp: pt.Common.CreateTime,
			Quesion:   pt.Details.Text,
			User:      toUser(pt.Details.User),
			isHistory: msg.IsHistory || cachedHistory(pt.Common.MsgId),
		}, nil

	case *pb.WebcastControlMessage:
		return ControlEvent{
			MessageID:   pt.Common.MsgId,
			Timestamp:   pt.Common.CreateTime,
			Action:      int(pt.Action),
			Description: pt.Action.String(),
			isHistory:   msg.IsHistory || cachedHistory(pt.Common.MsgId),
		}, nil

	case *pb.WebcastLinkMicBattle:
		users := []*User{}
		for _, u := range pt.HostTeam {
			groups := u.HostGroup
			for _, group := range groups {
				for _, user := range group.Host {
					urls := make([]string, 5)
					for _, img := range user.Images {
						urls = append(urls, img.UrlList...)
					}
					users = append(users, &User{
						ID:       int64(user.Id),
						Username: user.ProfileId,
						Nickname: user.Name,
						ProfilePicture: &ProfilePicture{
							Urls: urls,
						},
					})

				}

			}
		}
		return MicBattleEvent{
			MessageID: pt.Common.MsgId,
			Timestamp: pt.Common.CreateTime,
			Users:     users,
			isHistory: msg.IsHistory || cachedHistory(pt.Common.MsgId),
		}, nil

	case *pb.WebcastLinkMicArmies:
		battles := []*Battle{}
		for _, b := range pt.BattleItems {
			battle := &Battle{
				Host:   int64(b.HostUserId),
				Groups: []*BattleGroup{},
			}
			for _, g := range b.BattleGroups {
				group := BattleGroup{
					Points: int(g.Points),
					Users:  []*User{},
				}
				for _, u := range g.Users {
					group.Users = append(group.Users, toUser(u))
				}
				battle.Groups = append(battle.Groups, &group)
			}
			battles = append(battles, battle)
		}
		return BattlesEvent{
			MessageID: pt.Common.MsgId,
			Timestamp: pt.Common.CreateTime,
			Status:    int(pt.BattleStatus),
			Battles:   battles,
			isHistory: msg.IsHistory || cachedHistory(pt.Common.MsgId),
		}, nil
	case *pb.WebcastLiveIntroMessage:
		return IntroEvent{
			MessageID: pt.Common.MsgId,
			Timestamp: pt.Common.CreateTime,
			ID:        int(pt.RoomId),
			Title:     pt.Content,
			User:      toUser(pt.Host),
			isHistory: msg.IsHistory || cachedHistory(pt.Common.MsgId),
		}, nil

	case *pb.WebcastInRoomBannerMessage:
		var data interface{}
		// TODO: should we make a type for this instead of unmarshalling to see it is an error then feeding it up?
		err = json.Unmarshal([]byte(pt.GetJson()), &data)
		if err != nil {
			return nil, fmt.Errorf("WebcastInRoomBannerMessage: %w\n%s", err, data)
		}

		return RoomBannerEvent{
			MessageID: pt.Header.MsgId,
			Timestamp: pt.Header.CreateTime,
			Data:      data,
			isHistory: msg.IsHistory || cachedHistory(pt.Header.MsgId),
		}, nil

	default:
		base := base64.RawStdEncoding.EncodeToString(msg.Payload)
		err = fmt.Errorf("unimplemented type %T\n%s", m, base)
		debugHandler(err)
		warnHandler(fmt.Sprintf("unimplemented type %T", m))
		return nil, nil
	}
}

func cachedHistory(id int64) bool {
	_, present := msgIDCache.GetOrSet(id, struct{}{}, imcache.WithExpiration(messageHistoryTimeout))
	return present
}

func defaultLogHandler(i ...interface{}) {
	slog.Debug(fmt.Sprint(i...), "logger", "gotiktoklive-default")
}

func routineErrHandler(err ...interface{}) {
	slog.Debug(fmt.Sprint(err...), "logger", "gotiktoklive-default")
}

func toRecipientID(r *pb.WebcastGiftMessage_UserGiftReciever) int64 {
	if r == nil {
		return 0
	}
	return int64(r.UserId)
}

// badgeTextLabel returns the human-readable label of a badge's TextBadge.
//
// TikTok's live schema puts the rendered label (e.g. "No. 3" for Top Gifter,
// "New gifter") in field 2, which the vendored .proto does not declare — it
// only knows field 3 ("defaultPattern"). The value therefore arrives as an
// unknown field and is dropped by the generated getter, which is why such
// badges rendered with an icon but no number. Prefer field 2, fall back to the
// declared field 3.
func badgeTextLabel(t *pb.BadgeStruct_TextBadge) string {
	if t == nil {
		return ""
	}
	if s := textBadgeLabelFromUnknown(t.ProtoReflect().GetUnknown()); s != "" {
		return s
	}
	return t.GetDefaultPattern()
}

// textBadgeLabelFromUnknown walks raw protobuf bytes and returns field 2 as a
// string. Unknown bytes are appended verbatim, so a hand-rolled walk is the
// only way to read a field the descriptor does not know about.
func textBadgeLabelFromUnknown(b []byte) string {
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return ""
		}
		b = b[n:]
		switch typ {
		case protowire.BytesType:
			v, n := protowire.ConsumeBytes(b)
			if n < 0 {
				return ""
			}
			if num == 2 {
				return string(v)
			}
			b = b[n:]
		case protowire.VarintType:
			_, n := protowire.ConsumeVarint(b)
			if n < 0 {
				return ""
			}
			b = b[n:]
		case protowire.Fixed32Type:
			_, n := protowire.ConsumeFixed32(b)
			if n < 0 {
				return ""
			}
			b = b[n:]
		case protowire.Fixed64Type:
			_, n := protowire.ConsumeFixed64(b)
			if n < 0 {
				return ""
			}
			b = b[n:]
		default:
			return ""
		}
	}
	return ""
}

func toUser(u *pb.User) *User {
	if u == nil {
		return &User{}
	}
	username := u.IdStr
	if u.IdStr == "" {
		username = u.Nickname
	}
	user := User{
		ID:       int64(u.Id),
		Username: username,
		Nickname: u.Nickname,
	}

	// PATCH (upstream bug, issue #18): the original code tested
	// `u.AvatarLarge != nil` but then read `u.AvatarJpg.UrlList`. TikTok
	// usually sends only avatarThumb, so the guard almost always failed and
	// every user arrived with an empty profile picture. Pick the largest
	// image that actually has URLs, in descending quality order.
	for _, img := range []*pb.Image{u.AvatarLarge, u.AvatarMedium, u.AvatarThumb, u.AvatarJpg} {
		if img != nil && len(img.UrlList) > 0 {
			user.ProfilePicture = &ProfilePicture{Urls: img.UrlList}
			break
		}
	}

	user.ExtraAttributes = &ExtraAttributes{
		FollowRole: int(u.UserRole),
	}

	// PATCH (upstream bug, issue #18): upstream stored badge.String(), a
	// protobuf debug dump, as the badge name, and no image URL at all, so
	// widgets had nothing renderable. Expose the fields a badge needs:
	// image URL, label and background colour.
	if len(u.BadgeList) > 0 {
		var badges []*UserBadge
		for _, badge := range u.BadgeList {
			b := &UserBadge{Type: badge.GetDisplayType().String()}
			switch t := badge.GetBadgeType().(type) {
			case *pb.BadgeStruct_Combine:
				c := t.Combine
				if c.GetIcon() != nil && len(c.GetIcon().UrlList) > 0 {
					b.Image = c.GetIcon().UrlList[len(c.GetIcon().UrlList)-1]
				}
				if c.GetBackground() != nil {
					b.Color = c.GetBackground().BackgroundColorCode
				}
				b.Name = c.GetStr()
				if b.Name == "" {
					b.Name = badgeTextLabel(c.GetText())
				}
			case *pb.BadgeStruct_Image:
				if t.Image.GetImage() != nil && len(t.Image.GetImage().UrlList) > 0 {
					b.Image = t.Image.GetImage().UrlList[len(t.Image.GetImage().UrlList)-1]
				}
			case *pb.BadgeStruct_Text:
				b.Name = t.Text.GetDefaultPattern()
			case *pb.BadgeStruct_Str:
				b.Name = t.Str.GetStr()
			}
			badges = append(badges, b)
		}

		// PATCH (upstream gap): TikTok sends the same artwork twice for some
		// badges — a BADGEDISPLAYTYPE_IMAGE entry with no label, plus a
		// BADGEDISPLAYTYPE_COMBINE entry carrying the label ("No. 3" for Top
		// Gifter). Rendering both drew a duplicated icon, and the labelled one
		// looked blank before the text-label fix above. Collapse entries that
		// share an image, keeping the one that actually has a label.
		badges = dedupeBadges(badges)
		user.Badge = &BadgeAttributes{
			Badges: badges,
		}
	}
	return &user
}

// dedupeBadges collapses badges that share the same image URL, preferring the
// entry that carries a label and a colour. Order of first appearance is kept so
// the payload's badge ordering (grade first, then Top Gifter) survives.
func dedupeBadges(in []*UserBadge) []*UserBadge {
	if len(in) < 2 {
		return in
	}
	index := make(map[string]int, len(in))
	out := make([]*UserBadge, 0, len(in))
	for _, b := range in {
		if b == nil {
			continue
		}
		if b.Image == "" {
			out = append(out, b)
			continue
		}
		if i, ok := index[b.Image]; ok {
			// Keep whichever entry is richer.
			if out[i].Name == "" && b.Name != "" {
				out[i].Name = b.Name
			}
			if out[i].Color == "" && b.Color != "" {
				out[i].Color = b.Color
			}
			continue
		}
		index[b.Image] = len(out)
		out = append(out, b)
	}
	return out
}

func toUserIdentity(uid *pb.UserIdentity) *UserIdentity {
	if uid == nil {
		return nil
	}
	return &UserIdentity{
		IsGiftGiver:       uid.IsGiftGiverOfAnchor,
		IsSubscriber:      uid.IsSubscriberOfAnchor,
		IsMutualFollowing: uid.IsMutualFollowingWithAnchor,
		IsFollower:        uid.IsFollowerOfAnchor,
		IsModerator:       uid.IsModeratorOfAnchor,
		IsAnchor:          uid.IsAnchor,
	}
}

func copyMap(m map[string]string) map[string]string {
	out := make(map[string]string)
	for key, value := range m {
		out[key] = value
	}
	return out
}

func toUserType(displayType string) userEventType {
	switch displayType {
	case "pm_main_follow_message_viewer_2":
		return USER_FOLLOW
	case "pm_mt_guidance_share":
		return USER_SHARE
	case "live_room_enter_toast":
		return USER_JOIN
	case "JOINED":
		return USER_JOIN
	}
	return userEventType(fmt.Sprintf("User type not implemented, please report: %s", displayType))
}
