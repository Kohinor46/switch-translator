package unity

import "strings"

// Общие строки деревьев типов Unity: смещение с флагом 0x80000000 указывает сюда.
const commonStrings = "AABB\x00AnimationClip\x00AnimationCurve\x00AnimationState\x00Array\x00Base\x00BitField\x00bitset\x00bool\x00char\x00ColorRGBA\x00Component\x00data\x00deque\x00double\x00dynamic_array\x00FastPropertyName\x00first\x00float\x00Font\x00GameObject\x00Generic Mono\x00GradientNEW\x00GUID\x00GUIStyle\x00int\x00list\x00long long\x00map\x00Matrix4x4f\x00MdFour\x00MonoBehaviour\x00MonoScript\x00m_ByteSize\x00m_Curve\x00m_EditorClassIdentifier\x00m_EditorHideFlags\x00m_Enabled\x00m_ExtensionPtr\x00m_GameObject\x00m_Index\x00m_IsArray\x00m_IsStatic\x00m_MetaFlag\x00m_Name\x00m_ObjectHideFlags\x00m_PrefabInternal\x00m_PrefabParentObject\x00m_Script\x00m_StaticEditorFlags\x00m_Type\x00m_Version\x00Object\x00pair\x00PPtr<Component>\x00PPtr<GameObject>\x00PPtr<Material>\x00PPtr<MonoBehaviour>\x00PPtr<MonoScript>\x00PPtr<Object>\x00PPtr<Prefab>\x00PPtr<Sprite>\x00PPtr<TextAsset>\x00PPtr<Texture>\x00PPtr<Texture2D>\x00PPtr<Transform>\x00Prefab\x00Quaternionf\x00Rectf\x00RectInt\x00RectOffset\x00second\x00set\x00short\x00size\x00SInt16\x00SInt32\x00SInt64\x00SInt8\x00staticvector\x00string\x00TextAsset\x00TextMesh\x00Texture\x00Texture2D\x00Transform\x00TypelessData\x00UInt16\x00UInt32\x00UInt64\x00UInt8\x00unsigned int\x00unsigned long long\x00unsigned short\x00vector\x00Vector2f\x00Vector3f\x00Vector4f\x00m_ScriptingClassIdentifier\x00Gradient\x00Type*\x00int2_storage\x00int3_storage\x00BoundsInt\x00m_CorrespondingSourceObject\x00m_PrefabInstance\x00m_PrefabAsset\x00FileSize\x00Hash128\x00RenderingLayerMask\x00"

var commonIndex = func() map[uint32]string {
	m := map[uint32]string{}
	off := 0
	for _, s := range strings.Split(strings.TrimSuffix(commonStrings, "\x00"), "\x00") {
		m[uint32(off)] = s
		off += len(s) + 1
	}
	return m
}()

func commonString(off uint32) string { return commonIndex[off] }
