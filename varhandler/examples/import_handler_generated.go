// Code generated by "varhandler -func Import"; DO NOT EDIT

package main

import "net/http"
import z "github.com/azr/generators/varhandler/examples/z"

func ImportHandler(w http.ResponseWriter, r *http.Request) {
	var err error

	param0, err := HTTPX(r)
	if err != nil {
		HandleHttpErrorWithDefaultStatus(w, r, http.StatusBadRequest, err)
		return
	}

	param1, err := HTTPY(r)
	if err != nil {
		HandleHttpErrorWithDefaultStatus(w, r, http.StatusBadRequest, err)
		return
	}

	param2, err := HTTPZ(r)
	if err != nil {
		HandleHttpErrorWithDefaultStatus(w, r, http.StatusBadRequest, err)
		return
	}

	param3, err := z.HTTPZ(r)
	if err != nil {
		HandleHttpErrorWithDefaultStatus(w, r, http.StatusBadRequest, err)
		return
	}

	err = Import(param0, param1, param2, param3)
	if err != nil {
		HandleHttpErrorWithDefaultStatus(w, r, http.StatusInternalServerError, err)
		return
	}

}
