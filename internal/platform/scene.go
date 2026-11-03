package platform

type Scene string

const (
	SceneC2C   Scene = "c2c"   //	单聊
	SceneGroup Scene = "group" // 群聊，暂不实现
)

type ConversationKey struct {
	AppID   string
	Scene   Scene
	SceneID string
}

func (k ConversationKey) String() string {
	return k.AppID + "/" + string(k.Scene) + "/" + k.SceneID
}

type Sender struct {
	OpenID   string
	Nickname string
}
