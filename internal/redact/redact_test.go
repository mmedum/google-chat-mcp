package redact

import "testing"

func TestAccountKeepsTheDomainAndDropsTheRest(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"ann.petersen@example.com", "…@example.com"},
		{"a@b.example.com", "…@b.example.com"},
		// Already masked: idempotent, which is what lets a redactor
		// downstream of this one still recognise it.
		{"…@example.com", "…@example.com"},
		// Not an address: left alone rather than mangled.
		{"", ""},
		{"not-an-address", "not-an-address"},
		{"@nolocal.example.com", "@nolocal.example.com"},
		{"nodomain@", "nodomain@"},
	} {
		if got := Account(c.in); got != c.want {
			t.Errorf("Account(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Accounts is what an error message goes through, so it has to find an
// address inside somebody else's prose and leave the prose alone.
func TestAccountsMasksInsideAMessage(t *testing.T) {
	got := Accounts("The user ann@example.com does not have permission.")
	if got != "The user …@example.com does not have permission." {
		t.Errorf("Accounts = %q", got)
	}
	if plain := Accounts("File not found."); plain != "File not found." {
		t.Errorf("a message with no address should be untouched, got %q", plain)
	}
	// Idempotent, and that is not cosmetic: the "…" is in the pattern's
	// local-part class so that an address masked once is still FOUND by
	// a redactor running downstream. Take it out and the mask hides the
	// address from the next redactor instead of from the reader, which
	// is how masking more once leaked more.
	once := Accounts("The user ann@example.com was refused.")
	if twice := Accounts(once); twice != once {
		t.Errorf("masking twice changed the text:\n once  %q\n twice %q", once, twice)
	}
	if !address.MatchString(once) {
		t.Error("an already-masked address is not matched by the pattern, so a redactor " +
			"downstream of this one would not see it")
	}
}
