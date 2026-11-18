package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// HandOverReader reads a customer's reply to one of the two messages HO:RA
// sends when a supporter drops out of an errand (channel/handover.go): the
// errand was cancelled ("Want it done by someone else?"), or another
// supporter took it over. It only labels the words; the router decides what
// happens, and nothing it returns books, cancels or charges anything — a
// redo still ends in a summary the customer answers YES to.
type HandOverReader interface {
	ReadHandOver(ctx context.Context, q HandOverQuery) (HandOverReading, error)
}

// HandOverQuery is one reply and the notice it follows.
type HandOverQuery struct {
	// Kind: "cancelled" or "reassigned".
	Kind string
	// Notice: the message the customer received, word for word.
	Notice string
	Text   string
}

// HandOverReading is what a reply to a hand-over notice is.
type HandOverReading string

const (
	ReadRedo     HandOverReading = "redo"     // cancelled: do it again, with someone else
	ReadDecline  HandOverReading = "decline"  // cancelled: no, not again
	ReadWhy      HandOverReading = "why"      // what happened, why
	ReadCharged  HandOverReading = "charged"  // was I charged / will I pay twice
	ReadAck      HandOverReading = "ack"      // ok, thanks
	ReadStatus   HandOverReading = "status"   // reassigned: who is it, when do they come
	ReadCancel   HandOverReading = "cancel"   // reassigned: cancel the errand
	ReadOriginal HandOverReading = "original" // reassigned: wants the first supporter back
	ReadUnclear  HandOverReading = "unclear"  // about the notice, none of the above
	ReadOther    HandOverReading = "other"    // not about the notice
)

// HandOverReadings are the labels a reply to kind may take; any other label
// from the model is read as unclear.
func HandOverReadings(kind string) []HandOverReading {
	if kind == "cancelled" {
		return []HandOverReading{ReadRedo, ReadDecline, ReadWhy, ReadCharged, ReadAck, ReadUnclear, ReadOther}
	}
	return []HandOverReading{ReadAck, ReadStatus, ReadWhy, ReadCharged, ReadCancel, ReadOriginal, ReadUnclear, ReadOther}
}

const handOverPrompt = `HO:RA is an errand service in New York City: a vetted local "supporter" does an errand for a customer, who books and talks to HO:RA by text message. You read ONE text message a customer sent after HO:RA told them that their supporter dropped out of their errand. The customer may write in any language (often English, Chinese or Spanish). Return the one label that says what the message is.

What HO:RA told the customer is given below as "Notice". There are two kinds.

Kind "cancelled" — the errand was cancelled because the supporter couldn't finish it; the customer was not charged; HO:RA offered to have someone else do it. Labels:
- "redo": they want it done again, by someone else, or rebooked — including a plain yes to the offer ("yes", "sure", "please", "find someone else", "can you do it tomorrow instead?", "好的", "麻烦再找一个人", "sí, por favor", "búscame a otra persona").
- "decline": they don't want it done again ("no thanks", "never mind", "I'll do it myself", "不用了", "算了", "no gracias", "ya no hace falta").
- "why": they ask why, or what happened ("why?", "what happened?", "为什么", "怎么回事", "¿qué pasó?").
- "charged": they ask whether they were charged or what they pay ("was I charged?", "do I pay anything?", "扣钱了吗", "¿me cobraron?").
- "ack": a thanks or an acknowledgement with no question and no answer to the offer ("ok", "thanks", "got it", "谢谢", "gracias").

Kind "reassigned" — another supporter is now doing the errand, which is still on. Labels:
- "ack": a thanks or an acknowledgement ("ok", "thanks", "great", "好的", "谢谢", "vale, gracias").
- "status": who the new supporter is, when they come, where they are, whether the errand is still happening ("who is it?", "when will they arrive?", "是谁", "什么时候到", "¿quién es?", "¿cuándo llega?").
- "why": why the supporter changed, what happened to the first one.
- "charged": whether they pay twice, or what they pay now ("will I be charged twice?", "会收两次钱吗", "¿me van a cobrar dos veces?").
- "cancel": they want to cancel the errand ("cancel it", "I don't want it anymore", "不要了，取消吧", "cancélalo", "ya no lo quiero").
- "original": they want the first supporter back, don't want a different supporter, or object to the change of person ("I want the original one", "I don't want a different supporter", "我要原来那个人", "quiero a la persona de antes").

Both kinds:
- "unclear": about the notice, but none of the labels above fits: a complaint, a demand for compensation or a refund for a loss, a question you cannot place, or several of the labels at once that conflict.
- "other": not about the notice at all: a new, different errand ("can you also get me coffee?"), words meant for the supporter about the job (directions, a door code, "tell Sam the side door"), or anything unrelated.

Rules:
- A message that both acknowledges and asks takes the label of what it asks ("ok thanks, who is it?" is "status").
- Only use labels listed for the notice's kind; anything else about the notice is "unclear".
- Never guess a label from the notice alone: read the customer's message.

Return {"reading": "<label>"}.`

func handOverSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"reading": map[string]any{"type": "string", "enum": []string{
				string(ReadRedo), string(ReadDecline), string(ReadWhy), string(ReadCharged), string(ReadAck),
				string(ReadStatus), string(ReadCancel), string(ReadOriginal), string(ReadUnclear), string(ReadOther)}},
		},
		"required":             []string{"reading"},
		"additionalProperties": false,
	}
}

func readHandOver(ctx context.Context, m Provider, q HandOverQuery) (HandOverReading, error) {
	user := fmt.Sprintf("Kind: %s\nNotice: %q\nCustomer's message: %q", q.Kind, strings.TrimSpace(q.Notice), strings.TrimSpace(q.Text))
	out, _, err := m.CompleteJSON(ctx, handOverPrompt, user, "hand_over_reply", handOverSchema(), 256)
	if err != nil {
		return ReadOther, fmt.Errorf("read hand-over reply: %w", err)
	}
	var r struct {
		Reading HandOverReading `json:"reading"`
	}
	if err := json.Unmarshal([]byte(extractJSONObject(out)), &r); err != nil {
		return ReadOther, fmt.Errorf("read hand-over reply decode: %w", err)
	}
	for _, ok := range HandOverReadings(q.Kind) {
		if r.Reading == ok {
			return r.Reading, nil
		}
	}
	return ReadUnclear, nil
}

func (m *Model) ReadHandOver(ctx context.Context, q HandOverQuery) (HandOverReading, error) {
	return readHandOver(ctx, m.provider(), q)
}
