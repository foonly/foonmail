package hdr

import "testing"

func TestDecode(t *testing.T) {
	for in, want := range map[string]string{
		"plain subject": "plain subject",
		"=?windows-1252?Q?Grattis!_Din_f=F6delsedagsm=E5nad_=E4r_h=E4r!?=":                                      "Grattis! Din födelsedagsmånad är här!",
		"=?windows-1252?Q?Club_One_har_f=E5tt_ett_lyft_=96_har_du_h=E4ngt?= =?windows-1252?Q?_med=3F?=":         "Club One har fått ett lyft – har du hängt med?",
		"=?windows-1252?Q?Italienska_veckor_p=E5_=D6stersj=F6n_under_sept?= =?windows-1252?Q?ember=96oktober?=": "Italienska veckor på Östersjön under september–oktober",
		"=?ISO-8859-15?Q?P=E4=E4sykoodisi?=":                                                                    "Pääsykoodisi",
		"=?UTF-8?B?w6TDtg==?= tail":                                                                             "äö tail",
		"=?x-unknown?Q?abc?=":                                                                                   "=?x-unknown?Q?abc?=",
		"=?UTF-8?Q?broken_without_end":                                                                          "=?UTF-8?Q?broken_without_end",
	} {
		if got := Decode(in); got != want {
			t.Errorf("Decode(%q) = %q, want %q", in, got, want)
		}
	}
}
