package channel

import (
	"regexp"
	"strings"
	"unicode"
)

// The voice layer's triggers, by rule (D-28): is the customer writing in
// another language, and do they sound stressed. Both read only the words;
// neither asks a model.

// Chinese scripts, as DetectLanguage names them. LangZh: Chinese with no
// character that tells the scripts apart — the reply mirrors the customer.
const (
	LangZhHant = "zh-hant"
	LangZhHans = "zh-hans"
	LangZh     = "zh"
)

// tradSimp: characters that differ between Traditional and Simplified, as
// pairs. Simplified forms that are also ordinary Traditional characters
// (里, 后, 干, 准, 于, 只, 台) are left out: they tell nothing apart.
const tradSimp = "這这個个們们嗎吗來来說说會会時时幫帮點点對对還还東东買买賣卖為为麼么沒没過过從从開开關关問问題题員员號号電电話话錢钱間间現现發发給给動动達达際际區区讓让謝谢請请預预約约單单寫写線线機机車车場场長长門门見见覺觉經经種种樣样難难處处應应該该認认識识歡欢訂订購购費费務务選选擇择確确備备鐘钟頭头邊边離离遠远營营業业傳传實实計计帶带與与淨净隊队隨随據据灣湾紐纽嗎吗飯饭雞鸡魚鱼湯汤藥药醫医診诊廳厅館馆貨货遞递郵邮寄寄衛卫個个們们幾几塊块張张條条聽听讀读書书學学習习覽览戲戏樂乐無无氣气愛爱媽妈爺爷輛辆區区縣县鎮镇鄰邻樓楼層层廣广權权"

var tradOnly, simpOnly = func() (map[rune]bool, map[rune]bool) {
	t, s := map[rune]bool{}, map[rune]bool{}
	rs := []rune(tradSimp)
	for i := 0; i+1 < len(rs); i += 2 {
		if rs[i] != rs[i+1] {
			t[rs[i]], s[rs[i+1]] = true, true
		}
	}
	return t, s
}()

// chineseScript counts the characters only Traditional and only Simplified
// would write.
func chineseScript(s string) (trad, simp int) {
	for _, c := range s {
		switch {
		case tradOnly[c]:
			trad++
		case simpOnly[c]:
			simp++
		}
	}
	return
}

// Common words that mark a message as not English, by language. A word
// that is also English ("a", "me", "no", "son") is not listed.
var langWords = map[string][]string{
	"es": strings.Fields(`hola gracias por necesito quiero puedes puede recoger recoja paquete mañana hoy ayer dónde donde cuándo cuando cuánto cuanto qué que el la los las un una unos del mi mis tu su con para pero también muy está están estoy tengo hay sí bueno vale lavandería ropa comida compras tienda calle señor señora buenos buenas días tardes noches ayuda llevar traer entregar dirección ahora`),
	"fr": strings.Fields(`bonjour merci s'il vous plaît je tu il elle nous vous est sont suis avec mais très aujourd'hui demain où quand combien quoi le les une des du mes besoin veux pouvez peux colis linge courses magasin rue oui d'accord salut aide apporter livrer adresse maintenant`),
	"it": strings.Fields(`ciao grazie favore ho bisogno voglio puoi può ritirare pacco domani oggi dove quando quanto cosa il lo gli della mio mia con anche molto sono sì buongiorno buonasera aiuto portare consegnare indirizzo adesso lavanderia spesa negozio`),
	"pt": strings.Fields(`olá obrigado obrigada por favor preciso quero pode buscar pacote amanhã hoje onde quando quanto o os uma da meu minha mas também muito estou tenho sim bom dia tarde noite ajuda levar trazer entregar endereço agora lavanderia compras loja rua você`),
	"de": strings.Fields(`hallo danke bitte ich du sie wir ist sind mit für aber auch sehr heute morgen wo wann wie viel der das ein eine mein meine brauche möchte können kannst paket abholen wäsche einkaufen laden straße ja hilfe bringen liefern adresse jetzt guten tag`),
}

// English function words: a message with more of these than of any other
// language's is English.
var enWords = strings.Fields(`the a an is are am i you he she we they my your his her our their to of in on at for with and but or not it this that what where when how much can could would will please need want pick up get bring drop deliver from today tomorrow tonight morning now thanks thank hi hello hey yes ok okay sure`)

var langSets = func() map[string]map[string]bool {
	m := map[string]map[string]bool{}
	for lang, ws := range langWords {
		m[lang] = map[string]bool{}
		for _, w := range ws {
			m[lang][w] = true
		}
	}
	m["en"] = map[string]bool{}
	for _, w := range enWords {
		m["en"][w] = true
	}
	return m
}()

var langTokenRE = regexp.MustCompile(`[\p{L}']+`)

// DetectLanguage is the customer's language when it is not English: "es",
// "fr", "it", "pt", "de", Chinese as LangZhHant / LangZhHans / LangZh,
// "ja", "ko", "ru", "ar", "he", "th", "hi", or "other" for another script;
// "" for English, or for words that say nothing (YES, 2, ok, an address).
func DetectLanguage(text string) string {
	var han, kana, hangul, cyr, arab, hebr, thai, deva, otherScript, latin int
	for _, c := range text {
		switch {
		case unicode.Is(unicode.Han, c):
			han++
		case unicode.In(c, unicode.Hiragana, unicode.Katakana):
			kana++
		case unicode.Is(unicode.Hangul, c):
			hangul++
		case unicode.Is(unicode.Cyrillic, c):
			cyr++
		case unicode.Is(unicode.Arabic, c):
			arab++
		case unicode.Is(unicode.Hebrew, c):
			hebr++
		case unicode.Is(unicode.Thai, c):
			thai++
		case unicode.Is(unicode.Devanagari, c):
			deva++
		case unicode.Is(unicode.Latin, c):
			latin++
		case unicode.IsLetter(c):
			otherScript++
		}
	}
	switch {
	case kana > 0:
		return "ja"
	case han > 0 && han >= latin/4:
		trad, simp := chineseScript(text)
		switch {
		case trad > simp:
			return LangZhHant
		case simp > trad:
			return LangZhHans
		}
		return LangZh
	case hangul > 0:
		return "ko"
	case cyr > 0:
		return "ru"
	case arab > 0:
		return "ar"
	case hebr > 0:
		return "he"
	case thai > 0:
		return "th"
	case deva > 0:
		return "hi"
	case otherScript > latin:
		return "other"
	}
	score := map[string]int{}
	words := langTokenRE.FindAllString(strings.ToLower(text), -1)
	for _, w := range words {
		for lang, set := range langSets {
			if set[w] {
				score[lang]++
			}
		}
	}
	best, bestN := "", 0
	for lang, n := range score {
		if lang != "en" && (n > bestN || n == bestN && lang < best) {
			best, bestN = lang, n
		}
	}
	// One word on its own ("gracias", "merci") is enough; otherwise two,
	// and more than the English ones.
	need := 2
	if len(words) == 1 {
		need = 1
	}
	if bestN >= need && bestN > score["en"] {
		return best
	}
	return ""
}

// Stress, by rule. Each pattern names itself in the log and the trace.
var stressRules = []struct {
	name string
	re   *regexp.Regexp
}{
	{"punctuation", regexp.MustCompile(`\?\s*\?|[?!]\s*[?!]\s*[?!]|\?!|!\?|？\s*？|！\s*！`)},
	{"waiting", regexp.MustCompile(`(?i)\b(?:still|been|keep|kept) waiting\b|\bwaiting (?:forever|so long|for ages|too long)\b|\btaking (?:so|too|forever|ages)\b|\bstill (?:no|not) (?:here|there|answer|reply|response|update)\b|\b(?:it'?s|its) been (?:an hour|hours|forever|ages)\b`)},
	{"anger", regexp.MustCompile(`(?i)\b(?:ridiculous|unacceptable|terrible|awful|horrible|worst|useless|pathetic|incompetent|scam|rip ?off|wtf|wth|ffs|furious|pissed|angry|annoyed|frustrat\w*|fed up|disappoint\w*|not happy|unhappy|upset|this is (?:crazy|insane|a joke|a mess)|are you (?:serious|kidding)|seriously\?)\b`)},
	{"complaint", regexp.MustCompile(`(?i)\b(?:complain\w*|charged (?:me )?twice|double[- ]charged|overcharged|wrong (?:order|item|address|amount)|never (?:came|arrived|showed)|didn'?t (?:show|come|arrive)|no one (?:is )?(?:answering|responding|replying|came)|nobody (?:is )?(?:answering|responding|replying|came)|(?:i'?m|i am|gonna be|going to be) (?:so |really )?late)\b`)},
	{"non_english", regexp.MustCompile(`太慢|怎么还|怎麼還|还没|還沒|搞什么|搞什麼|生气|生氣|投诉|投訴|qué pasa|todavía no|hasta cuándo|no puede ser|ya llevo`)},
}

// shoutWords: capitals that are words people type, not shouting.
var shoutOK = map[string]bool{"YES": true, "NO": true, "STOP": true, "HELP": true, "INFO": true, "SKIP": true, "LAST": true, "START": true,
	"NYC": true, "ASAP": true, "OK": true, "USPS": true, "UPS": true, "CVS": true, "ATM": true, "ID": true, "PM": true, "AM": true, "ZIP": true, "TJ": true, "HORA": true}

var shoutRE = regexp.MustCompile(`\b[A-Z]{3,}\b`)

// StressedBy names the rule that reads the customer as stressed or
// frustrated, "" when none does.
func StressedBy(text string) string {
	for _, r := range stressRules {
		if r.re.MatchString(text) {
			return r.name
		}
	}
	n := 0
	for _, w := range shoutRE.FindAllString(text, -1) {
		if !shoutOK[w] {
			n++
		}
	}
	if n >= 1 && strings.ToUpper(text) == text && len(strings.Fields(text)) >= 3 || n >= 2 {
		return "shouting"
	}
	return ""
}
