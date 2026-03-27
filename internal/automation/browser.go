package automation

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/proto"
)

// BrowserAutomation handles complex browser interactions.
type BrowserAutomation struct {
	page   *rod.Page
	logger *slog.Logger
}

// NewBrowserAutomation wraps a Rod page with automation helpers.
func NewBrowserAutomation(page *rod.Page, logger *slog.Logger) *BrowserAutomation {
	return &BrowserAutomation{
		page:   page,
		logger: logger.With("component", "browser_automation"),
	}
}

// --- Click & Hover ---

// Click clicks an element matched by the CSS selector.
func (ba *BrowserAutomation) Click(selector string) error {
	el, err := ba.page.Timeout(10 * time.Second).Element(selector)
	if err != nil {
		return fmt.Errorf("element not found: %s: %w", selector, err)
	}
	return el.Click(proto.InputMouseButtonLeft, 1)
}

// DoubleClick double-clicks an element.
func (ba *BrowserAutomation) DoubleClick(selector string) error {
	el, err := ba.page.Timeout(10 * time.Second).Element(selector)
	if err != nil {
		return fmt.Errorf("element not found: %s: %w", selector, err)
	}
	return el.Click(proto.InputMouseButtonLeft, 2)
}

// Hover hovers over an element.
func (ba *BrowserAutomation) Hover(selector string) error {
	el, err := ba.page.Timeout(10 * time.Second).Element(selector)
	if err != nil {
		return fmt.Errorf("element not found: %s: %w", selector, err)
	}
	return el.Hover()
}

// --- Form Interaction ---

// TypeText types text into an input field.
func (ba *BrowserAutomation) TypeText(selector, text string) error {
	el, err := ba.page.Timeout(10 * time.Second).Element(selector)
	if err != nil {
		return fmt.Errorf("element not found: %s: %w", selector, err)
	}
	el.MustSelectAllText()
	return el.Input(text)
}

// SelectOption selects an option from a <select> dropdown.
func (ba *BrowserAutomation) SelectOption(selector string, values []string) error {
	el, err := ba.page.Timeout(10 * time.Second).Element(selector)
	if err != nil {
		return err
	}
	return el.Select(values, true, rod.SelectorTypeText)
}

// SubmitForm submits a form by pressing Enter or clicking a submit button.
func (ba *BrowserAutomation) SubmitForm(formSelector string) error {
	// Try clicking submit button first
	submitBtn, err := ba.page.Element(formSelector + " [type='submit'], " + formSelector + " button[type='submit']")
	if err == nil {
		return submitBtn.Click(proto.InputMouseButtonLeft, 1)
	}

	// Fallback: press Enter on the form
	form, err := ba.page.Element(formSelector)
	if err != nil {
		return err
	}
	_ = form.Focus()
	return ba.page.Keyboard.Press(input.Enter)
}

// FillForm fills multiple form fields at once.
func (ba *BrowserAutomation) FillForm(fields map[string]string) error {
	for selector, value := range fields {
		if err := ba.TypeText(selector, value); err != nil {
			return fmt.Errorf("fill %s: %w", selector, err)
		}
		time.Sleep(100 * time.Millisecond) // Human-like delay
	}
	return nil
}

// --- Scrolling ---

// ScrollToBottom scrolls to the bottom of the page.
func (ba *BrowserAutomation) ScrollToBottom() error {
	_, err := ba.page.Eval(`window.scrollTo(0, document.body.scrollHeight)`)
	return err
}

// ScrollBy scrolls by a specific amount.
func (ba *BrowserAutomation) ScrollBy(x, y int) error {
	_, err := ba.page.Eval(fmt.Sprintf(`window.scrollBy(%d, %d)`, x, y))
	return err
}

// ScrollToElement scrolls an element into view.
func (ba *BrowserAutomation) ScrollToElement(selector string) error {
	el, err := ba.page.Element(selector)
	if err != nil {
		return err
	}
	return el.ScrollIntoView()
}

// InfiniteScroll handles infinite scroll pages by scrolling until no new content.
func (ba *BrowserAutomation) InfiniteScroll(maxScrolls int, waitBetween time.Duration) (int, error) {
	lastHeight := 0
	scrollCount := 0

	for scrollCount < maxScrolls {
		// Get current scroll height
		result, err := ba.page.Eval(`document.body.scrollHeight`)
		if err != nil {
			return scrollCount, err
		}
		currentHeight := result.Value.Int()

		if currentHeight == lastHeight {
			break // No new content loaded
		}
		lastHeight = currentHeight

		// Scroll to bottom
		if err := ba.ScrollToBottom(); err != nil {
			return scrollCount, err
		}
		scrollCount++

		// Wait for content to load
		time.Sleep(waitBetween)
	}

	return scrollCount, nil
}

// --- Login/Auth ---

// LoginCredentials holds login form data.
type LoginCredentials struct {
	UsernameSelector string
	PasswordSelector string
	SubmitSelector   string
	Username         string
	Password         string
}

// Login performs a login sequence.
func (ba *BrowserAutomation) Login(creds LoginCredentials) error {
	// Type username
	if err := ba.TypeText(creds.UsernameSelector, creds.Username); err != nil {
		return fmt.Errorf("type username: %w", err)
	}
	time.Sleep(200 * time.Millisecond)

	// Type password
	if err := ba.TypeText(creds.PasswordSelector, creds.Password); err != nil {
		return fmt.Errorf("type password: %w", err)
	}
	time.Sleep(200 * time.Millisecond)

	// Click submit
	if creds.SubmitSelector != "" {
		return ba.Click(creds.SubmitSelector)
	}

	// press Enter as fallback
	passField, err := ba.page.Element(creds.PasswordSelector)
	if err != nil {
		return err
	}
	_ = passField.Focus()
	return ba.page.Keyboard.Press(input.Enter)
}

// --- Pagination ---

// PaginationType specifies the pagination strategy.
type PaginationType string

const (
	PaginationClick  PaginationType = "click"
	PaginationScroll PaginationType = "scroll"
	PaginationURL    PaginationType = "url"
)

// PaginationConfig configures pagination handling.
type PaginationConfig struct {
	Type            PaginationType
	NextSelector    string // CSS selector for "next" button
	MaxPages        int
	WaitBetween     time.Duration
	ContentSelector string // Selector to check for new content
}

// HandlePagination iterates through paginated content.
func (ba *BrowserAutomation) HandlePagination(ctx context.Context, cfg PaginationConfig, callback func(pageNum int, html string) error) error {
	for page := 1; page <= cfg.MaxPages; page++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Get current page content
		html, err := ba.page.HTML()
		if err != nil {
			return err
		}

		if err := callback(page, html); err != nil {
			return err
		}

		// Navigate to next page
		switch cfg.Type {
		case PaginationClick:
			nextBtn, err := ba.page.Element(cfg.NextSelector)
			if err != nil {
				ba.logger.Info("no more pages", "last_page", page)
				return nil
			}
			if err := nextBtn.Click(proto.InputMouseButtonLeft, 1); err != nil {
				return err
			}

		case PaginationScroll:
			oldHeight, _ := ba.page.Eval(`document.body.scrollHeight`)
			if err := ba.ScrollToBottom(); err != nil {
				// Ignore scroll errors at bottom of infinite scroll
			}
			time.Sleep(cfg.WaitBetween)
			newHeight, _ := ba.page.Eval(`document.body.scrollHeight`)
			if oldHeight.Value.Int() == newHeight.Value.Int() {
				return nil
			}

		case PaginationURL:
			nextLink, err := ba.page.Element(cfg.NextSelector)
			if err != nil {
				return nil
			}
			href, err := nextLink.Attribute("href")
			if err != nil || href == nil || *href == "" {
				return nil
			}
			if err := ba.page.Navigate(*href); err != nil {
				return err
			}
		}

		time.Sleep(cfg.WaitBetween)
		_ = ba.page.WaitStable(300 * time.Millisecond)
	}

	return nil
}

// --- Network Interception ---

// InterceptConfig configures network request interception.
type InterceptConfig struct {
	URLPattern string
	Method     string
	Handler    func(req *proto.FetchRequestPaused) *proto.FetchFulfillRequest
}

// Screenshot captures a screenshot of the page.
func (ba *BrowserAutomation) Screenshot() ([]byte, error) {
	return ba.page.Screenshot(true, nil)
}

// WaitForNavigation waits for a page navigation to complete.
func (ba *BrowserAutomation) WaitForNavigation() error {
	return ba.page.WaitStable(500 * time.Millisecond)
}

// EvalJS executes JavaScript and returns the result.
func (ba *BrowserAutomation) EvalJS(js string) (string, error) {
	result, err := ba.page.Eval(js)
	if err != nil {
		return "", err
	}
	return result.Value.String(), nil
}

// --- Macro Recording ---

// Action represents a recorded browser action.
type Action struct {
	Type     string `json:"type"` // click, type, scroll, wait, navigate
	Selector string `json:"selector,omitempty"`
	Value    string `json:"value,omitempty"`
	X        int    `json:"x,omitempty"`
	Y        int    `json:"y,omitempty"`
	Delay    int    `json:"delay_ms,omitempty"`
}

// Macro is a sequence of recorded actions.
type Macro struct {
	Name    string   `json:"name"`
	Actions []Action `json:"actions"`
}

// PlayMacro replays a recorded macro.
func (ba *BrowserAutomation) PlayMacro(macro Macro) error {
	ba.logger.Info("playing macro", "name", macro.Name, "actions", len(macro.Actions))

	for i, action := range macro.Actions {
		if action.Delay > 0 {
			time.Sleep(time.Duration(action.Delay) * time.Millisecond)
		}

		var err error
		switch action.Type {
		case "click":
			err = ba.Click(action.Selector)
		case "type":
			err = ba.TypeText(action.Selector, action.Value)
		case "scroll":
			err = ba.ScrollBy(action.X, action.Y)
		case "wait":
			time.Sleep(time.Duration(action.Delay) * time.Millisecond)
		case "navigate":
			err = ba.page.Navigate(action.Value)
		case "hover":
			err = ba.Hover(action.Selector)
		case "screenshot":
			_, err = ba.Screenshot()
		default:
			ba.logger.Warn("unknown action type", "type", action.Type, "index", i)
		}

		if err != nil {
			return fmt.Errorf("macro action %d (%s): %w", i, action.Type, err)
		}
	}

	return nil
}
