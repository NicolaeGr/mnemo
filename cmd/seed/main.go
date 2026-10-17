// Command seed fills a throwaway database with fixtures for manual CardDAV
// testing: an admin account, Family, Work, School, and Medical books, a few
// dozen contacts spread across them, a few untagged contacts, and two device
// tokens.
package main

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"strings"

	"github.com/emersion/go-vcard"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicolaegr/mnemo/internal/config"
	"github.com/nicolaegr/mnemo/internal/model"
	"github.com/nicolaegr/mnemo/internal/store"
	vcardmeta "github.com/nicolaegr/mnemo/internal/vcard"
)

type bookSpec struct {
	slug, display string
	tiers         []string
	sort          int
}

type tel struct{ kind, num string }

type addr struct{ kind, street, city, region, postal, country string }

type person struct {
	prefix, given, additional, family, suffix string
	org, title, role                          string
	tels                                      []tel
	emails                                    []string
	adr                                       *addr
	bday, note, url                           string
}

var (
	maleNames = []string{"James", "Robert", "John", "Michael", "David", "William",
		"Richard", "Joseph", "Thomas", "Charles", "Daniel", "Matthew", "Anthony",
		"Mark", "Steven", "Paul", "Andrew", "Joshua", "Kenneth", "Kevin", "Brian",
		"George", "Edward", "Ronald", "Timothy"}
	femaleNames = []string{"Mary", "Patricia", "Jennifer", "Linda", "Elizabeth",
		"Barbara", "Susan", "Jessica", "Sarah", "Nancy", "Karen", "Lisa",
		"Margaret", "Sandra", "Ashley", "Kimberly", "Emily", "Donna", "Michelle",
		"Carol", "Amanda", "Melissa", "Deborah", "Stephanie", "Rebecca"}
	firstNames = append(append([]string{}, maleNames...), femaleNames...)
	lastNames  = []string{"Anderson", "Bianchi", "Kowalski", "Nakamura", "Okafor",
		"Petrov", "Rossi", "Silva", "Nguyen", "Haddad", "Larsen", "Novak",
		"Ferreira", "Yamamoto", "Osei", "Ivanov", "Costa", "Schmidt", "Dubois",
		"Moreau", "Andersen", "Vargas", "Mensah", "Bakker", "Ross", "Kaur",
		"Singh", "Ahmed", "Cohen", "Murray", "Whitfield", "Delacroix",
		"Marchetti", "Sorensen", "Kovacs", "Rahman", "Salazar", "Fitzgerald",
		"Lombardi", "Eriksson"}
	suffixes    = []string{"Jr.", "Sr.", "II", "III", "Ph.D."}
	professions = []string{"Electrician", "Chef", "Attorney", "Accountant",
		"Architect", "Carpenter", "Pharmacist", "Journalist", "Pilot", "Farmer",
		"Librarian", "Mechanic", "Social Worker", "Tailor", "Baker", "Painter"}
	streets = []string{"Paper Street", "Fictional Lane", "Placid Avenue",
		"Blank Road", "Maple Grove", "Oak Terrace", "Cedar Lane", "Birchwood Drive",
		"Willow Court", "Sycamore Way"}
	cities  = []string{"Anytown", "Springfield", "Fairview", "Riverside", "Lakeside", "Hillcrest"}
	regions = []string{"CA", "NY", "TX", "WA", "OR", "MA"}
	country = "United States of America"

	companies = []string{"Acme Corp", "Globex", "Initech", "Umbrella Health",
		"Northwind Traders", "Stark Industries", "Wayne Enterprises", "Soylent Co",
		"Hooli", "Vandelay Industries"}
	workTitles = []string{"Software Engineer", "Product Manager", "Data Analyst",
		"Sales Lead", "Operations Manager", "Designer", "Recruiter", "Accountant",
		"Support Engineer", "Team Lead"}
	schools = []string{"Springfield High", "Fairview Elementary",
		"Riverside Middle School", "Hillcrest Academy", "Lakeside College"}
	schoolRoles = []string{"Student", "Teacher", "Coach", "Counselor", "Principal"}
	clinics     = []string{"Anytown Family Clinic", "Riverside Medical Center",
		"Fairview Urgent Care", "Hillcrest Pediatrics", "Lakeside Dental"}
	specialties = []string{"Family Medicine", "Pediatrics", "Cardiology",
		"Dermatology", "Dentistry", "Orthopedics", "Neurology", "General Practice"}
)

func main() {
	ctx := context.Background()
	cfg := config.Load()

	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer st.Close()
	if err := store.Migrate(ctx, st.PG); err != nil {
		log.Fatalf("migrate: %v", err)
	}

	users := store.NewUsers(st.PG)
	admin, err := users.Signup(ctx, "admin", "admin@example.com", "Admin", "password", cfg.BcryptCost)
	if err != nil {
		log.Fatalf("signup: %v", err)
	}
	owner := model.Actor{UserID: admin.ID, Username: admin.Username}

	books := seedBooks(ctx, st.PG, owner, admin.ID)
	r := rand.New(rand.NewSource(7))

	idx := 0
	add := func(p person, bookIDs []int64) {
		idx++
		fn := filename(p, idx)
		if err := addPerson(ctx, st.PG, owner, admin.ID, p, fn, bookIDs); err != nil {
			log.Fatalf("seed %s: %v", fn, err)
		}
	}

	counts := map[string]int{}
	write := func(slug string, n int, gen func() person, slots []string) {
		for i := 0; i < n; i++ {
			ids := make([]int64, 0, len(slots))
			for _, s := range slots {
				ids = append(ids, books[s])
			}
			add(gen(), ids)
			counts[strings.Join(slots, "+")]++
		}
	}

	write("family", 22, func() person { return familyPerson(r) }, []string{"family"})
	write("work", 20, func() person { return workPerson(r) }, []string{"work"})
	write("school", 12, func() person { return schoolPerson(r) }, []string{"school"})
	write("medical", 10, func() person { return medicalPerson(r) }, []string{"medical"})
	write("general", 6, func() person { return generalPerson(r) }, nil)
	write("family+work", 3, func() person { return workPerson(r) }, []string{"family", "work"})

	principals := store.NewPrincipals(st.PG)
	android, androidToken, err := principals.IssueToken(ctx, admin.ID, "primary", "android")
	if err != nil {
		log.Fatalf("issue android token: %v", err)
	}
	thunderbird, thunderbirdToken, err := principals.IssueToken(ctx, admin.ID, "secondary", "thunderbird")
	if err != nil {
		log.Fatalf("issue thunderbird token: %v", err)
	}

	log.Printf("books: family=%d work=%d school=%d medical=%d",
		books["family"], books["work"], books["school"], books["medical"])
	for slug, n := range counts {
		log.Printf("contacts: %-12s %d", slug, n)
	}
	log.Printf("total contacts: %d", idx)
	log.Printf("android:     principal %d, tier primary   token %s", android.ID, androidToken)
	log.Printf("thunderbird: principal %d, tier secondary token %s", thunderbird.ID, thunderbirdToken)
}

func seedBooks(ctx context.Context, pool *pgxpool.Pool, owner model.Actor, adminID int64) map[string]int64 {
	specs := []bookSpec{
		{"family", "Family", []string{"primary", "secondary"}, 100},
		{"work", "Work", []string{"primary"}, 200},
		{"school", "School", []string{"secondary"}, 300},
		{"medical", "Medical", []string{"primary", "secondary"}, 400},
	}
	ids := map[string]int64{}
	err := store.WithTx(ctx, pool, owner, func(s store.ScopedStore) error {
		if err := store.LockSync(ctx, s.Tx, adminID); err != nil {
			return err
		}
		for _, b := range specs {
			book, err := s.CreateBook(ctx, b.slug, b.display, nil, b.sort)
			if err != nil {
				return err
			}
			if err := s.SetBookTiers(ctx, book.ID, b.tiers); err != nil {
				return err
			}
			ids[b.slug] = book.ID
		}
		return nil
	})
	if err != nil {
		log.Fatalf("seed books: %v", err)
	}
	return ids
}

func addPerson(ctx context.Context, pool *pgxpool.Pool, owner model.Actor, adminID int64, p person, filename string, bookIDs []int64) error {
	card := buildCard(p)
	text, uid, meta, err := vcardmeta.Prepare(card, filename)
	if err != nil {
		return err
	}
	return store.WithTx(ctx, pool, owner, func(s store.ScopedStore) error {
		if err := store.LockSync(ctx, s.Tx, adminID); err != nil {
			return err
		}
		_, err := s.SaveContact(ctx, store.PutContactParams{
			Filename: filename, UID: uid, VCardText: text, SearchMeta: meta,
		}, store.Precondition{}, bookIDs)
		return err
	})
}

func buildCard(p person) vcard.Card {
	card := vcard.Card{}
	card.SetValue(vcard.FieldVersion, "3.0")
	if p.given != "" || p.family != "" {
		card.SetValue(vcard.FieldName, strings.Join([]string{
			p.family, p.given, p.additional, p.prefix, p.suffix}, ";"))
	}
	if fn := strings.Join(strings.Fields(strings.Join([]string{
		p.prefix, p.given, p.additional, p.family, p.suffix}, " ")), " "); fn != "" {
		card.SetValue(vcard.FieldFormattedName, fn)
	} else if p.org != "" {
		card.SetValue(vcard.FieldFormattedName, p.org)
	}
	if p.org != "" {
		card.SetValue(vcard.FieldOrganization, p.org)
	}
	if p.title != "" {
		card.SetValue(vcard.FieldTitle, p.title)
	}
	if p.role != "" {
		card.SetValue(vcard.FieldRole, p.role)
	}
	for _, t := range p.tels {
		card.Add(vcard.FieldTelephone, &vcard.Field{
			Value:  t.num,
			Params: vcard.Params{vcard.ParamType: []string{t.kind}},
		})
	}
	for _, e := range p.emails {
		card.Add(vcard.FieldEmail, &vcard.Field{
			Value:  e,
			Params: vcard.Params{vcard.ParamType: []string{"HOME"}},
		})
	}
	if a := p.adr; a != nil {
		card.Add(vcard.FieldAddress, &vcard.Field{
			Value:  strings.Join([]string{"", "", a.street, a.city, a.region, a.postal, a.country}, ";"),
			Params: vcard.Params{vcard.ParamType: []string{a.kind}},
		})
	}
	if p.bday != "" {
		card.SetValue(vcard.FieldBirthday, p.bday)
	}
	if p.note != "" {
		card.SetValue(vcard.FieldNote, p.note)
	}
	if p.url != "" {
		card.SetValue(vcard.FieldURL, p.url)
	}
	return card
}

func familyPerson(r *rand.Rand) person {
	male := r.Intn(2) == 0
	given := pick(r, femaleNames)
	if male {
		given = pick(r, maleNames)
	}
	prefix := ""
	if r.Intn(2) == 0 {
		switch {
		case r.Intn(3) == 0:
			prefix = "Prof."
		case male:
			prefix = "Mr."
		default:
			prefix = pick(r, []string{"Mrs.", "Ms."})
		}
	}
	family := pick(r, lastNames)
	return person{
		prefix: prefix,
		given:  given,
		family: family,
		suffix: maybe(r, suffixes),
		title:  pick(r, professions),
		tels: []tel{
			{"HOME", phone(r)},
			{"CELL", phone(r)},
		},
		emails: []string{email(given, family)},
		adr:    homeAddr(r),
		bday:   birthday(r),
	}
}

func workPerson(r *rand.Rand) person {
	given, family := pick(r, firstNames), pick(r, lastNames)
	return person{
		given:  given,
		family: family,
		org:    pick(r, companies),
		title:  pick(r, workTitles),
		tels: []tel{
			{"WORK", phone(r)},
			{"CELL", phone(r)},
		},
		emails: []string{email(given, family), strings.ToLower(sanitize(given)) + "@" + sanitize(pick(r, companies)) + ".com"},
		adr: &addr{
			kind:    "WORK",
			street:  fmt.Sprintf("%d %s", r.Intn(9000)+100, pick(r, streets)),
			city:    pick(r, cities),
			region:  pick(r, regions),
			postal:  postal(r),
			country: country,
		},
	}
}

func schoolPerson(r *rand.Rand) person {
	given, family := pick(r, firstNames), pick(r, lastNames)
	return person{
		given:  given,
		family: family,
		org:    pick(r, schools),
		title:  pick(r, schoolRoles),
		tels:   []tel{{"CELL", phone(r)}},
		emails: []string{email(given, family)},
		note:   "School contact",
	}
}

func medicalPerson(r *rand.Rand) person {
	given, family := pick(r, firstNames), pick(r, lastNames)
	if r.Intn(3) == 0 {
		clinic := pick(r, clinics)
		return person{
			org:    clinic,
			title:  pick(r, specialties),
			tels:   []tel{{"WORK", phone(r)}, {"WORK", phone(r)}},
			emails: []string{strings.ToLower(sanitize(clinic)) + "@example.com"},
			adr: &addr{
				kind:    "WORK",
				street:  fmt.Sprintf("%d %s", r.Intn(9000)+100, pick(r, streets)),
				city:    pick(r, cities),
				region:  pick(r, regions),
				postal:  postal(r),
				country: country,
			},
			role: "Clinic",
		}
	}
	return person{
		prefix: "Dr.",
		given:  given,
		family: family,
		org:    pick(r, clinics),
		title:  pick(r, specialties),
		tels:   []tel{{"WORK", phone(r)}},
		emails: []string{email(given, family)},
		role:   "Physician",
	}
}

func generalPerson(r *rand.Rand) person {
	given, family := pick(r, firstNames), pick(r, lastNames)
	return person{
		given:  given,
		family: family,
		tels:   []tel{{"CELL", phone(r)}},
		emails: []string{email(given, family)},
	}
}

func homeAddr(r *rand.Rand) *addr {
	return &addr{
		kind:    "HOME",
		street:  fmt.Sprintf("%d %s", r.Intn(9000)+100, pick(r, streets)),
		city:    pick(r, cities),
		region:  pick(r, regions),
		postal:  postal(r),
		country: country,
	}
}

func filename(p person, idx int) string {
	base := sanitize(p.given + "-" + p.family)
	if base == "" {
		base = sanitize(p.org)
	}
	if base == "" {
		base = "contact"
	}
	return fmt.Sprintf("%s-%d.vcf", base, idx)
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

func email(given, family string) string {
	return sanitize(given) + "." + sanitize(family) + "@example.com"
}

func phone(r *rand.Rand) string {
	return fmt.Sprintf("+1 (555) %03d-%04d", r.Intn(900)+100, r.Intn(10000))
}

func postal(r *rand.Rand) string {
	return fmt.Sprintf("%05d", r.Intn(90000)+10000)
}

func birthday(r *rand.Rand) string {
	return fmt.Sprintf("%04d-%02d-%02d", 1950+r.Intn(50), r.Intn(12)+1, r.Intn(28)+1)
}

func pick(r *rand.Rand, xs []string) string { return xs[r.Intn(len(xs))] }

func maybe(r *rand.Rand, xs []string) string {
	if r.Intn(3) == 0 {
		return pick(r, xs)
	}
	return ""
}
