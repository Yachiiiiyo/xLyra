package market

// BuiltinRootKeys 是根密钥仪式（2026-10-25）产出的两把根公钥，A 主用、B 备用，见 08 第 9 节。
var BuiltinRootKeys = []string{
	"jOpl0qOO2+Zj36iuSzoKUY/EzeRoW05XYLYazWM+MFs=", // SHA256:/q6wZqVA1H+3lZK3i8+QRBGYXXm0k09Ph5IRIhszvNc=
	"DL73FbAqc8uEqbf1FNXrbJqroqlodHGTvjLMol5bNNg=", // SHA256:Gs291BrEw+x6TAusI3/IEfXqGpB72zTp8qIeF4PYtEg=
}

var BuiltinRegistryURLs = []string{
	"https://xlyra-registry.yachiyo.im/v1/",
}
