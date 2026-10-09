package gotiktoklive

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand"
	"strconv"
	"strings"
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
			Emotes:       toEmotes(pt.EmotesList),
			Timestamp:    pt.Common.CreateTime,
			isHistory:    msg.IsHistory || cachedHistory(pt.Common.MsgId),
		}, nil
	case *pb.WebcastEmoteChatMessage:
		// A standalone subscriber emote: no comment text, just artwork.
		return EmoteEvent{
			MessageID:    pt.Common.MsgId,
			Timestamp:    pt.Common.CreateTime,
			User:         toUser(pt.User),
			UserIdentity: toUserIdentity(pt.UserIdentity),
			Emotes:       toEmoteList(pt.EmoteList),
			isHistory:    msg.IsHistory || cachedHistory(pt.Common.MsgId),
		}, nil
	case *pb.WebcastMemberMessage:
		return UserEvent{
			MessageID: pt.Common.MsgId,
			Timestamp: pt.Common.CreateTime,
			Event:     toUserType(pt.Action.String()),
			User:      toUser(pt.User),
			ActionID:  int(pt.GetAction()),
			isHistory: msg.IsHistory || cachedHistory(pt.Common.MsgId),
		}, nil
	case *pb.WebcastSubNotifyMessage:
		// A subscription notice (new sub or renewal). TikTok delivers it as its
		// own message instead of a WebcastMemberMessage, and upstream had no
		// case for it, so every subscribe was dropped before it reached a
		// widget — which already renders a `subscribe` alert. The event was
		// documented in protocol.md but never produced.
		return UserEvent{
			MessageID: pt.Common.MsgId,
			Timestamp: pt.Common.CreateTime,
			Event:     USER_SUBSCRIBE,
			User:      toUser(pt.User),
			isHistory: msg.IsHistory || cachedHistory(pt.Common.MsgId),
		}, nil
	case *pb.WebcastBarrageMessage:
		// Super Fan notices arrive as a barrage ("banner") message. Upstream
		// had no case for this message at all, so every one was dropped.
		// TikTok Live Connector identifies them by two markers: content.key and
		// commonBarrageContent.key. The latter is field 24, which the vendored
		// descriptor does not declare, so it is read straight off the wire.
		keys := []string{pt.GetContent().GetKey()}
		if raw := unknownBytesField(pt.ProtoReflect().GetUnknown(), 24); raw != nil {
			var t pb.Text
			if err := proto.Unmarshal(raw, &t); err == nil {
				keys = append(keys, t.GetKey())
			}
		}
		kind, ok := superFanKindFromKeys(keys)
		if !ok {
			// A barrage that is not a Super Fan notice (an ordinary system
			// banner). There is no widget event for it.
			return nil, nil
		}
		// The sender sits in field 50 (base.user.User), also undeclared here.
		var user *User
		if raw := unknownBytesField(pt.ProtoReflect().GetUnknown(), 50); raw != nil {
			var u pb.User
			if err := proto.Unmarshal(raw, &u); err == nil {
				user = toUser(&u)
			}
		}
		return SuperFanEvent{
			MessageID: pt.GetCommon().GetMsgId(),
			Timestamp: pt.GetCommon().GetCreateTime(),
			Event:     kind,
			User:      user,
			isHistory: msg.IsHistory || cachedHistory(pt.GetCommon().GetMsgId()),
		}, nil
	case *pb.WebcastEnvelopeMessage:
		// A Super Fan Box is an envelope. The connector matches either the
		// display-text key or businessType == SUPER_FAN_BOX. That enum value is
		// 19 and the vendored enums.pb.go stops at 7, so it is compared as a
		// plain integer (proto3 keeps the numeric value either way).
		info := pt.GetEnvelopeInfo()
		isBox := strings.Contains(strings.ToLower(pt.GetCommon().GetDisplayText().GetKey()), "ttlive_superfanbox") ||
			int32(info.GetBusinessType()) == envelopeBusinessTypeSuperFanBox
		if !isBox {
			// Any other envelope (diamonds, portal, …) is not a Super Fan Box.
			return nil, nil
		}
		return SuperFanEvent{
			MessageID:    pt.GetCommon().GetMsgId(),
			Timestamp:    pt.GetCommon().GetCreateTime(),
			Event:        SUPER_FAN_BOX,
			User:         envelopeUser(info),
			DiamondCount: int(info.GetDiamondCount()),
			isHistory:    msg.IsHistory || cachedHistory(pt.GetCommon().GetMsgId()),
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
			MessageID:  pt.Common.MsgId,
			Timestamp:  pt.Common.CreateTime,
			Viewers:    int(pt.Total),
			TopViewers: toTopViewers(pt.RanksList),
			isHistory:  msg.IsHistory || cachedHistory(pt.Common.MsgId),
		}, nil
	case *pb.WebcastSocialMessage:
		ev := UserEvent{
			MessageID: pt.Common.MsgId,
			Timestamp: pt.Common.CreateTime,
			Event:     toUserType(pt.Common.DisplayText.Key),
			User:      toUser(pt.User),
			isHistory: msg.IsHistory || cachedHistory(pt.Common.MsgId),
		}
		// TikTok Live Connector exposes the social message's display text as
		// displayType/label (its WebcastMessageEventDetails). The vendored
		// proto keeps them on Common.displayText.
		if dt := pt.Common.GetDisplayText(); dt != nil {
			ev.DisplayType = dt.GetKey()
			ev.Label = dt.GetDefaultPattern()
		}
		return ev, nil
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

// unknownBytesField walks raw protobuf bytes and returns the payload of the
// first field with the given number, or nil when absent. Unknown bytes are
// stored verbatim, so a hand-rolled walk is the only way to read a field the
// vendored descriptor does not declare (used here for WebcastBarrageMessage's
// commonBarrageContent = 24 and user = 50).
func unknownBytesField(b []byte, want protowire.Number) []byte {
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return nil
		}
		b = b[n:]
		switch typ {
		case protowire.BytesType:
			v, n := protowire.ConsumeBytes(b)
			if n < 0 {
				return nil
			}
			if num == want {
				return v
			}
			b = b[n:]
		case protowire.VarintType:
			_, n := protowire.ConsumeVarint(b)
			if n < 0 {
				return nil
			}
			b = b[n:]
		case protowire.Fixed32Type:
			_, n := protowire.ConsumeFixed32(b)
			if n < 0 {
				return nil
			}
			b = b[n:]
		case protowire.Fixed64Type:
			_, n := protowire.ConsumeFixed64(b)
			if n < 0 {
				return nil
			}
			b = b[n:]
		default:
			return nil
		}
	}
	return nil
}

// envelopeBusinessTypeSuperFanBox is EnvelopeBusinessType.SUPER_FAN_BOX (19).
// The vendored enums.pb.go stops at 7 (BusinessTypeFanClubGtM), but proto3 keeps
// the numeric value on the wire, so a plain integer compare still works.
const envelopeBusinessTypeSuperFanBox int32 = 19

// superFanKindFromKeys classifies a barrage by its display-text keys, matching
// TikTok Live Connector's rule: "superfanjoined" wins over the generic
// "ttlive_superfan" marker. Case-insensitive; returns ok=false when neither
// marker is present (an ordinary banner).
func superFanKindFromKeys(keys []string) (superFanKind, bool) {
	joined, plain := false, false
	for _, k := range keys {
		k = strings.ToLower(k)
		if strings.Contains(k, "ttlive_superfan_commentnotif_superfanjoined") {
			joined = true
		} else if strings.Contains(k, "ttlive_superfan") {
			plain = true
		}
	}
	switch {
	case joined:
		return SUPER_FAN_JOIN, true
	case plain:
		return SUPER_FAN_NEW, true
	}
	return "", false
}

// envelopeUser builds a User from a Super Fan Box envelope, which identifies the
// sender by the flat sendUser* fields rather than a nested User message.
func envelopeUser(info *pb.WebcastEnvelopeMessage_EnvelopeInfo) *User {
	if info == nil {
		return &User{}
	}
	avatar := ""
	if img := info.GetSendUserAvatar(); img != nil && len(img.GetUrlList()) > 0 {
		avatar = img.GetUrlList()[len(img.GetUrlList())-1]
	}
	u := &User{
		Username: info.GetSendUserId(),
		Nickname: info.GetSendUserName(),
	}
	// sendUserId is a numeric string; the widget's userId field comes from the
	// int64 ID, so carry it over when it parses (otherwise it would read "0").
	if id, err := strconv.ParseInt(info.GetSendUserId(), 10, 64); err == nil {
		u.ID = id
	}
	if avatar != "" {
		u.ProfilePicture = &ProfilePicture{Urls: []string{avatar}}
	}
	return u
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

	// PATCH (upstream gap): identity/profile fields TikTok Live Connector
	// exposes. Forwarded so the payload matches TLC instead of carrying only
	// the name and avatar.
	user.SecUid = u.GetSecUid()
	user.CreateTime = u.GetCreateTime()
	user.BioDescription = u.GetBioDescription()
	if u.GetAvatarThumb() != nil {
		user.ProfilePictureUrls = u.GetAvatarThumb().GetUrlList()
	} else if u.GetAvatarMedium() != nil {
		user.ProfilePictureUrls = u.GetAvatarMedium().GetUrlList()
	} else if u.GetAvatarLarge() != nil {
		user.ProfilePictureUrls = u.GetAvatarLarge().GetUrlList()
	}
	if fi := u.GetFollowInfo(); fi != nil {
		user.FollowInfo = &FollowInfo{
			FollowingCount: fi.GetFollowingCount(),
			FollowerCount:  fi.GetFollowerCount(),
			FollowStatus:   fi.GetFollowStatus(),
			PushStatus:     fi.GetPushStatus(),
		}
	}
	// TLC's gifterLevel: the user's spend grade (PayGrade). The badge artwork
	// for it is scene 8; the level itself lives on PayGrade.
	if pg := u.GetPayGrade(); pg != nil {
		user.GifterLevel = pg.GetLevel()
	}

	// PATCH (upstream gap): surface fan-club membership so the sidecar can
	// forward it. Prefer FansClub.data (carries club name AND level); fall back
	// to FansClubInfo.fansLevel, which TikTok still sends when clubName is empty.
	if fc := u.GetFansClub(); fc != nil && fc.GetData() != nil {
		user.FansClubName = fc.GetData().GetClubName()
		user.FansClubLevel = int(fc.GetData().GetLevel())
	}
	if fci := u.GetFansClubInfo(); fci != nil && fci.GetFansLevel() > 0 {
		if user.FansClubLevel == 0 {
			user.FansClubLevel = int(fci.GetFansLevel())
		}
	}

	// PATCH (upstream gap): the fan-club badge artwork. TikTok puts the
	// member's fan-club medal on User.medal; fansClubInfo.badge carries the
	// same image when medal is absent. This is the badge the widget's fan-club
	// filter keys on.
	if m := u.GetMedal(); m != nil && len(m.UrlList) > 0 {
		user.FanClubBadge = m.UrlList[len(m.UrlList)-1]
	}
	if user.FanClubBadge == "" {
		if fci := u.GetFansClubInfo(); fci != nil && fci.GetBadge() != nil && len(fci.GetBadge().UrlList) > 0 {
			user.FanClubBadge = fci.GetBadge().UrlList[len(fci.GetBadge().UrlList)-1]
		}
	}

	// PATCH: fan-club dormancy ("grey badge"). Only POSITIVE evidence of
	// dormancy clears the flag: a payload that omits the status still counts
	// as an active member, so a live member is never mistaken for a former
	// one. Set only when the user actually belongs to a club.
	if user.FansClubLevel > 0 || user.FansClubName != "" || user.FanClubBadge != "" {
		user.FanClubActive = true
		if fc := u.GetFansClub(); fc != nil && fc.GetData() != nil &&
			fc.GetData().GetUserFansClubStatus() == pb.User_FansClub_FansClubData_INACTIVE {
			user.FanClubActive = false
		}
		if fci := u.GetFansClubInfo(); fci != nil && fci.GetIsSleeping() {
			user.FanClubActive = false
		}
	}

	// PATCH (upstream bug, issue #18): upstream stored badge.String(), a
	// protobuf debug dump, as the badge name, and no image URL at all, so
	// widgets had nothing renderable. Expose the fields a badge needs:
	// image URL, label and background colour.
	if len(u.BadgeList) > 0 {
		var badges []*UserBadge
		for _, badge := range u.BadgeList {
			b := &UserBadge{Type: badge.GetDisplayType().String()}
			b.DisplayType = int(badge.GetDisplayType())
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
			// Fill the scene type the widget filters on, derived from the artwork.
			if b.SceneType == 0 {
				b.SceneType = badgeSceneFromURL(b.Image)
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

// toTopViewers flattens the roomUser rank list into TLC's topViewers shape:
// [{user: {...}, coinCount: N}].
func toTopViewers(list []*pb.WebcastRoomUserSeqMessage_Contributor) []TopViewer {
	if len(list) == 0 {
		return nil
	}
	out := make([]TopViewer, 0, len(list))
	for _, c := range list {
		if c == nil || c.GetUser() == nil {
			continue
		}
		out = append(out, TopViewer{
			User:      toUser(c.GetUser()),
			CoinCount: int64(c.GetScore()),
		})
	}
	return out
}

// toEmotes flattens the comment's inline emotes. TikTok puts a placeholder
// character in the comment text for each emote and stores the artwork here; the
// index is 0-based (TikTok Live Connector: "placeInComment ... starting at 0").
func toEmotes(list []*pb.WebcastChatMessage_EmoteWithIndex) []Emote {
	if len(list) == 0 {
		return nil
	}
	out := make([]Emote, 0, len(list))
	for _, e := range list {
		if e == nil {
			continue
		}
		em := toEmote(e.GetEmote())
		em.PlaceInComment = int(e.GetIndex())
		out = append(out, em)
	}
	return out
}

// toEmoteList flattens a standalone subscriber emote list (no comment index).
func toEmoteList(list []*pb.Emote) []Emote {
	if len(list) == 0 {
		return nil
	}
	out := make([]Emote, 0, len(list))
	for _, e := range list {
		if e == nil {
			continue
		}
		out = append(out, toEmote(e))
	}
	return out
}

// toEmote maps the protobuf Emote to the flattened event shape, picking the
// largest image URL that actually exists (same reasoning as the avatar fix).
func toEmote(e *pb.Emote) Emote {
	if e == nil {
		return Emote{}
	}
	em := Emote{
		EmoteID:     e.GetEmoteId(),
		EmoteType:   int(e.GetEmoteType()),
		PrivateType: int(e.GetEmotePrivateType()),
	}
	if img := e.GetImage(); img != nil && len(img.UrlList) > 0 {
		em.ImageURL = img.UrlList[len(img.UrlList)-1]
	}
	return em
}

// dedupeBadges collapses badges that share the same image URL, preferring the
// entry that carries a label and a colour. Order of first appearance is kept so
// the payload's badge ordering (grade first, then Top Gifter) survives.
// badgeSceneFromURL derives TikFinity's badgeSceneType from the badge artwork
// URL. TikTok's proto has no scene field, so without this every badge the
// bridge forwards carries badgeSceneType 0 and a widget cannot tell a fan-club
// badge (10) from a grade (8). The artwork filenames are stable.
func badgeSceneFromURL(url string) int {
	switch {
	case strings.Contains(url, "fans_badge_icon"):
		return 10 // fan club
	case strings.Contains(url, "grade_badge_icon"):
		return 8 // grade
	case strings.Contains(url, "moderater_badge_icon"):
		return 1 // moderator
	}
	return 0
}

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
	case "SUBSCRIBED":
		// MemberMessageAction_SUBSCRIBED.String(): a subscribe that arrives as
		// a WebcastMemberMessage rather than a WebcastSubNotifyMessage. Without
		// this it fell through to the "not implemented" fallback and the
		// sidecar's type switch dropped it.
		return USER_SUBSCRIBE
	}
	return userEventType(fmt.Sprintf("User type not implemented, please report: %s", displayType))
}
